package meetups

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"tropa/internal/auth"
)

// OfferTTL — сколько ждём ответа на предложенное место.
const OfferTTL = 24 * time.Hour

// PStatus — положение участника на встрече.
type PStatus string

const (
	Confirmed PStatus = "confirmed"
	Waitlist  PStatus = "waitlist"
	Offered   PStatus = "offered" // место освободилось, ждём ответа до OfferExpiresAt
)

var pstatusLabels = map[PStatus]string{Confirmed: "Идёт", Waitlist: "В очереди", Offered: "Предложено место"}

func (s PStatus) Label() string { return pstatusLabels[s] }

type Participant struct {
	UserID         int64
	Name           string
	Status         PStatus
	Position       int // место в очереди, с 1; 0 — не в очереди
	OfferExpiresAt time.Time
}

var (
	ErrClosed    = errors.New("запись на встречу закрыта")
	ErrNotJoined = errors.New("вы не записаны на встречу")
	ErrOrganizer = errors.New("организатор не может выйти из своей встречи — её можно отменить")
	ErrNoOffer   = errors.New("предложения места нет или оно истекло")
)

// Join записывает пользователя: есть свободное место — confirmed, нет — в конец
// очереди. Всё — под блокировкой строки встречи, поэтому одновременные записи
// выстраиваются по одной и мест не раздаётся больше, чем есть.
// Повторная запись ничего не меняет и возвращает текущий статус.
func (s *Service) Join(ctx context.Context, u *auth.User, id int64) (PStatus, error) {
	now := s.Now()
	var st PStatus
	err := pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		m, err := lockMeetup(ctx, tx, id)
		if err != nil {
			return err
		}
		if p, err := participant(ctx, tx, id, u.ID); err == nil {
			st = p.Status
			return nil
		} else if !errors.Is(err, ErrNotJoined) {
			return err
		}
		if m.Status != Open || m.Past(now) {
			return ErrClosed
		}
		if m.Free() != 0 {
			st = Confirmed
			_, err = tx.Exec(ctx, `INSERT INTO participants (meetup_id, user_id, status) VALUES ($1, $2, 'confirmed')`, id, u.ID)
			return err
		}
		st = Waitlist
		_, err = tx.Exec(ctx,
			`INSERT INTO participants (meetup_id, user_id, status, position)
			 SELECT $1, $2, 'waitlist', coalesce(max(position), 0) + 1 FROM participants WHERE meetup_id = $1`,
			id, u.ID)
		return err
	})
	return st, err
}

// Leave — выйти со встречи или из очереди. Освободившееся место
// предлагается первому в очереди. Отказ от предложенного места — тоже Leave.
func (s *Service) Leave(ctx context.Context, u *auth.User, id int64) error {
	now := s.Now()
	return pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		m, err := lockMeetup(ctx, tx, id)
		if err != nil {
			return err
		}
		if m.OrganizerID == u.ID {
			return ErrOrganizer
		}
		p, err := participant(ctx, tx, id, u.ID)
		if err != nil {
			return err
		}
		if err := remove(ctx, tx, id, p); err != nil {
			return err
		}
		if p.Status != Waitlist {
			m.Going--
			return fillSeats(ctx, tx, m, now)
		}
		return nil
	})
}

// Accept — принять предложенное место, пока предложение не истекло.
func (s *Service) Accept(ctx context.Context, u *auth.User, id int64) error {
	now := s.Now()
	return pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		if _, err := lockMeetup(ctx, tx, id); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx,
			`UPDATE participants SET status = 'confirmed', offer_expires_at = NULL
			  WHERE meetup_id = $1 AND user_id = $2 AND status = 'offered' AND offer_expires_at > $3`,
			id, u.ID, now)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrNoOffer
		}
		return nil
	})
}

// ExpireOffers снимает просроченные предложения и передаёт места дальше
// по очереди. Возвращает, сколько предложений снято. Зовётся фоновым воркером.
func (s *Service) ExpireOffers(ctx context.Context) (int, error) {
	now := s.Now()
	rows, err := s.DB.Query(ctx,
		`SELECT DISTINCT meetup_id FROM participants WHERE status = 'offered' AND offer_expires_at <= $1`, now)
	if err != nil {
		return 0, err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return 0, err
	}
	total := 0
	for _, id := range ids {
		err := pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
			m, err := lockMeetup(ctx, tx, id)
			if err != nil {
				return err
			}
			// условие повторяем под блокировкой: предложение могли успеть принять
			tag, err := tx.Exec(ctx,
				`DELETE FROM participants WHERE meetup_id = $1 AND status = 'offered' AND offer_expires_at <= $2`, id, now)
			if err != nil {
				return err
			}
			n := int(tag.RowsAffected())
			total += n
			m.Going -= n
			return fillSeats(ctx, tx, m, now)
		})
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

// fillSeats предлагает свободные места очереди по порядку: offered на OfferTTL.
// m — встреча, заблокированная в tx, с актуальными Going и Capacity.
// У отменённой и прошедшей встречи очередь не двигается.
func fillSeats(ctx context.Context, tx pgx.Tx, m *Meetup, now time.Time) error {
	if m.Status == Cancelled || m.Past(now) {
		return nil
	}
	for m.Free() != 0 {
		var userID int64
		err := tx.QueryRow(ctx,
			`SELECT user_id FROM participants WHERE meetup_id = $1 AND status = 'waitlist' ORDER BY position LIMIT 1`,
			m.ID).Scan(&userID)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil // очередь пуста
		}
		if err != nil {
			return err
		}
		p, err := participant(ctx, tx, m.ID, userID)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx,
			`UPDATE participants SET status = 'offered', position = NULL, offer_expires_at = $3
			  WHERE meetup_id = $1 AND user_id = $2`, m.ID, userID, now.Add(OfferTTL)); err != nil {
			return err
		}
		if err := closeGap(ctx, tx, m.ID, p.Position); err != nil {
			return err
		}
		m.Going++
	}
	return nil
}

// remove удаляет участника; если он стоял в очереди — сдвигает очередь.
func remove(ctx context.Context, tx pgx.Tx, meetupID int64, p *Participant) error {
	if _, err := tx.Exec(ctx, `DELETE FROM participants WHERE meetup_id = $1 AND user_id = $2`, meetupID, p.UserID); err != nil {
		return err
	}
	if p.Status == Waitlist {
		return closeGap(ctx, tx, meetupID, p.Position)
	}
	return nil
}

// closeGap сдвигает очередь за освободившейся позицией pos, чтобы позиции
// шли подряд с 1. Уникальность позиций проверяется в конце транзакции.
func closeGap(ctx context.Context, tx pgx.Tx, meetupID int64, pos int) error {
	_, err := tx.Exec(ctx,
		`UPDATE participants SET position = position - 1 WHERE meetup_id = $1 AND status = 'waitlist' AND position > $2`,
		meetupID, pos)
	return err
}

func participant(ctx context.Context, q querier, meetupID, userID int64) (*Participant, error) {
	p := &Participant{UserID: userID}
	var pos *int
	var exp *time.Time
	err := q.QueryRow(ctx,
		`SELECT p.status, p.position, p.offer_expires_at, u.name
		   FROM participants p JOIN users u ON u.id = p.user_id
		  WHERE p.meetup_id = $1 AND p.user_id = $2`, meetupID, userID).Scan(&p.Status, &pos, &exp, &p.Name)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotJoined
	}
	if err != nil {
		return nil, err
	}
	if pos != nil {
		p.Position = *pos
	}
	if exp != nil {
		p.OfferExpiresAt = *exp
	}
	return p, nil
}

// Participant — запись пользователя на встречу; ErrNotJoined, если её нет.
func (s *Service) Participant(ctx context.Context, meetupID, userID int64) (*Participant, error) {
	return participant(ctx, s.DB, meetupID, userID)
}

// Participants — все участники: идущие по времени записи, затем очередь по порядку.
func (s *Service) Participants(ctx context.Context, meetupID int64) ([]Participant, error) {
	rows, err := s.DB.Query(ctx,
		`SELECT p.user_id, u.name, p.status, coalesce(p.position, 0), coalesce(p.offer_expires_at, 'epoch')
		   FROM participants p JOIN users u ON u.id = p.user_id
		  WHERE p.meetup_id = $1
		  ORDER BY p.status = 'waitlist', p.position, p.joined_at, p.user_id`, meetupID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Participant, error) {
		var p Participant
		err := row.Scan(&p.UserID, &p.Name, &p.Status, &p.Position, &p.OfferExpiresAt)
		return p, err
	})
}
