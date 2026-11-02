package meetups

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"time"
	_ "time/tzdata" // Asia/Almaty без системной базы поясов (Windows, distroless)

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"tropa/internal/auth"
)

type Service struct {
	DB  *pgxpool.Pool
	Now func() time.Time
	// SiteURL — адрес сайта без / в конце, для ссылок в уведомлениях.
	SiteURL string
}

func New(db *pgxpool.Pool) *Service { return &Service{DB: db, Now: time.Now} }

// newToken — 16 случайных байт в base64url: ссылку на встречу «для друзей» не подобрать.
func newToken() string {
	b := make([]byte, 16)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// Create создаёт встречу на месте placeID; организатор сразу записан.
// При ошибках проверки — ErrInvalidInput и ошибки по полям.
func (s *Service) Create(ctx context.Context, organizer *auth.User, placeID int64, in Input) (*Meetup, map[string]string, error) {
	f, errs := in.Validate(s.Now())
	if len(errs) > 0 {
		return nil, errs, ErrInvalidInput
	}
	var id int64
	err := pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx,
			`INSERT INTO meetups (token, place_id, organizer_id, title, description, starts_at, capacity, visibility)
			 VALUES ($1, $2, $3, $4, $5, $6, NULLIF($7, 0), $8) RETURNING id`,
			newToken(), placeID, organizer.ID, f.title, f.description, f.startsAt, f.capacity, f.visibility).Scan(&id)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO participants (meetup_id, user_id, status) VALUES ($1, $2, 'confirmed')`,
			id, organizer.ID)
		return err
	})
	if err != nil {
		return nil, nil, fmt.Errorf("создание встречи: %w", err)
	}
	m, err := s.Get(ctx, id)
	return m, nil, err
}

// Update меняет поля встречи. Права, статус и число записанных проверяются
// под блокировкой строки встречи — той же, что берёт запись.
func (s *Service) Update(ctx context.Context, u *auth.User, id int64, in Input) (map[string]string, error) {
	now := s.Now()
	f, errs := in.Validate(now)
	if len(errs) > 0 {
		return errs, ErrInvalidInput
	}
	err := pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		m, err := lockMeetup(ctx, tx, id)
		if err != nil {
			return err
		}
		if !m.CanManage(u) {
			return ErrForbidden
		}
		if !m.Editable(now) {
			return ErrNotEditable
		}
		if f.capacity > 0 && f.capacity < m.Going {
			errs = map[string]string{"capacity": fmt.Sprintf("Уже записано %d — меньше мест сделать нельзя", m.Going)}
			return ErrInvalidInput
		}
		_, err = tx.Exec(ctx,
			`UPDATE meetups SET title = $2, description = $3, starts_at = $4, capacity = NULLIF($5, 0),
			        visibility = $6, updated_at = now()
			  WHERE id = $1`,
			id, f.title, f.description, f.startsAt, f.capacity, f.visibility)
		if err != nil {
			return err
		}
		moved := !f.startsAt.Equal(m.StartsAt)
		m.Title, m.StartsAt = f.title, f.startsAt
		if moved {
			if err := s.notifyAll(ctx, tx, m, "moved", s.movedText(m)); err != nil {
				return err
			}
		}
		// мест стало больше — отдаём их очереди
		m.Capacity = f.capacity
		return s.fillSeats(ctx, tx, m, now)
	})
	if errors.Is(err, ErrInvalidInput) {
		return errs, err
	}
	return nil, err
}

// SetStatus переводит встречу из from в to. from — статус, который видел
// организатор: если встреча успела смениться, вернётся ErrStaleStatus.
func (s *Service) SetStatus(ctx context.Context, u *auth.User, id int64, from, to Status) error {
	if !from.CanBecome(to) {
		return ErrBadStatus
	}
	now := s.Now()
	return pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		m, err := lockMeetup(ctx, tx, id)
		if err != nil {
			return err
		}
		if !m.CanManage(u) {
			return ErrForbidden
		}
		if m.Status != from {
			return ErrStaleStatus
		}
		if !m.Editable(now) {
			return ErrNotEditable
		}
		if _, err = tx.Exec(ctx, `UPDATE meetups SET status = $2, updated_at = now() WHERE id = $1`, id, to); err != nil {
			return err
		}
		if to == Cancelled {
			return s.notifyAll(ctx, tx, m, "cancelled", s.cancelledText(m))
		}
		return nil
	})
}

// Get — встреча по id (без проверки доступа).
func (s *Service) Get(ctx context.Context, id int64) (*Meetup, error) {
	return scanMeetup(s.DB.QueryRow(ctx, `SELECT `+meetupColumns+meetupFrom+` WHERE m.id = $1`, id))
}

// ByToken — встреча по ссылке /m/{token}. Неверный токен — ErrNotFound.
func (s *Service) ByToken(ctx context.Context, token string) (*Meetup, error) {
	if len(token) != 22 {
		return nil, ErrNotFound
	}
	return scanMeetup(s.DB.QueryRow(ctx, `SELECT `+meetupColumns+meetupFrom+` WHERE m.token = $1`, token))
}

type querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

const meetupColumns = `m.id, m.token, m.place_id, p.slug, p.name, p.category, m.organizer_id, u.name,
	m.title, m.description, m.starts_at, coalesce(m.capacity, 0), m.visibility, m.status, m.created_at,
	(SELECT count(*) FROM participants x WHERE x.meetup_id = m.id AND x.status <> 'waitlist'),
	(SELECT count(*) FROM participants x WHERE x.meetup_id = m.id AND x.status = 'waitlist')`

const meetupFrom = ` FROM meetups m JOIN places p ON p.id = m.place_id JOIN users u ON u.id = m.organizer_id`

func scanMeetup(row pgx.Row) (*Meetup, error) {
	m := &Meetup{}
	err := row.Scan(&m.ID, &m.Token, &m.PlaceID, &m.PlaceSlug, &m.PlaceName, &m.PlaceCategory,
		&m.OrganizerID, &m.OrganizerName, &m.Title, &m.Description, &m.StartsAt, &m.Capacity,
		&m.Visibility, &m.Status, &m.CreatedAt, &m.Going, &m.Waiting)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return m, err
}

// lockMeetup блокирует строку встречи до конца транзакции и читает её
// уже после блокировки, со свежими счётчиками участников.
func lockMeetup(ctx context.Context, tx pgx.Tx, id int64) (*Meetup, error) {
	var one int
	err := tx.QueryRow(ctx, `SELECT 1 FROM meetups WHERE id = $1 FOR UPDATE`, id).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return scanMeetup(tx.QueryRow(ctx, `SELECT `+meetupColumns+meetupFrom+` WHERE m.id = $1`, id))
}
