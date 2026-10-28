// Package meetups — встречи на местах карты: создание, правка, статусы,
// запись с листом ожидания, списки.
package meetups

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata" // Asia/Almaty без системной базы поясов (Windows, distroless)
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"tropa/internal/auth"
)

// Almaty — пояс, в котором пользователь вводит и видит время встречи.
var Almaty = mustLoad("Asia/Almaty")

func mustLoad(name string) *time.Location {
	loc, err := time.LoadLocation(name)
	if err != nil {
		panic(err)
	}
	return loc
}

const (
	DateLayout = "2006-01-02"
	TimeLayout = "15:04"
	// MaxCapacity — потолок мест; без лимита — пустое поле.
	MaxCapacity = 200
)

type Status string

const (
	Open      Status = "open"
	Closed    Status = "closed"
	Cancelled Status = "cancelled"
)

var statusLabels = map[Status]string{Open: "Идёт запись", Closed: "Запись закрыта", Cancelled: "Отменена"}

func (s Status) Label() string { return statusLabels[s] }

// transitions — переходы, которые организатор делает вручную.
var transitions = map[Status][]Status{
	Open:   {Closed, Cancelled},
	Closed: {Open, Cancelled},
}

// Next — куда организатор может перевести встречу из статуса s.
func (s Status) Next() []Status { return transitions[s] }

func (s Status) CanBecome(to Status) bool { return slices.Contains(transitions[s], to) }

var actionLabels = map[Status]string{Open: "Открыть запись", Closed: "Закрыть запись", Cancelled: "Отменить встречу"}

func (s Status) Action() string { return actionLabels[s] }

type Visibility string

const (
	Public  Visibility = "public"
	Friends Visibility = "friends"
)

var Visibilities = []Visibility{Public, Friends}

var visibilityLabels = map[Visibility]string{Public: "Открытая — видна всем на карте", Friends: "Для друзей — только по ссылке"}

func (v Visibility) Label() string { return visibilityLabels[v] }

type Meetup struct {
	ID            int64
	Token         string
	PlaceID       int64
	PlaceSlug     string
	PlaceName     string
	PlaceCategory string
	OrganizerID   int64
	OrganizerName string
	Title         string
	Description   string
	StartsAt      time.Time
	Capacity      int // 0 — без лимита
	Visibility    Visibility
	Status        Status
	Going         int // идут или держат предложенное место (confirmed + offered)
	Waiting       int // в очереди
	CreatedAt     time.Time
}

// URL — ссылка на встречу; одна и та же для открытых и «для друзей».
func (m *Meetup) URL() string { return "/m/" + m.Token }

// Free — сколько мест свободно; -1 — без лимита.
func (m *Meetup) Free() int {
	if m.Capacity == 0 {
		return -1
	}
	return max(m.Capacity-m.Going, 0)
}

// Past — встреча уже началась.
func (m *Meetup) Past(now time.Time) bool { return !m.StartsAt.After(now) }

// CanManage — организатор встречи или админ.
func (m *Meetup) CanManage(u *auth.User) bool {
	return u != nil && (u.ID == m.OrganizerID || u.IsAdmin())
}

// Editable — можно ли править поля и статус: не отменена и ещё не началась.
func (m *Meetup) Editable(now time.Time) bool { return m.Status != Cancelled && !m.Past(now) }

// Input — поля формы встречи строками, как пришли из запроса.
type Input struct {
	Title, Description string
	Date, Time         string // по Алматы
	Capacity           string // пусто — без лимита
	Visibility         string
}

// FromMeetup заполняет форму значениями существующей встречи.
func FromMeetup(m *Meetup) Input {
	in := Input{
		Title: m.Title, Description: m.Description,
		Date: m.StartsAt.In(Almaty).Format(DateLayout), Time: m.StartsAt.In(Almaty).Format(TimeLayout),
		Visibility: string(m.Visibility),
	}
	if m.Capacity > 0 {
		in.Capacity = strconv.Itoa(m.Capacity)
	}
	return in
}

// fields — проверенные значения формы.
type fields struct {
	title, description string
	startsAt           time.Time
	capacity           int // 0 — без лимита
	visibility         Visibility
}

// Validate проверяет форму. now — текущее время: встреча должна быть
// в будущем, но не дальше чем через год.
func (in Input) Validate(now time.Time) (fields, map[string]string) {
	var f fields
	errs := map[string]string{}

	f.title = strings.TrimSpace(in.Title)
	if n := utf8.RuneCountInString(f.title); n < 3 || n > 120 {
		errs["title"] = "Название — от 3 до 120 символов"
	}
	f.description = strings.TrimSpace(in.Description)
	if utf8.RuneCountInString(f.description) > 5000 {
		errs["description"] = "Описание — не длиннее 5000 символов"
	}

	t, err := time.ParseInLocation(DateLayout+" "+TimeLayout, strings.TrimSpace(in.Date)+" "+strings.TrimSpace(in.Time), Almaty)
	switch {
	case err != nil:
		errs["starts_at"] = "Укажите дату и время"
	case !t.After(now):
		errs["starts_at"] = "Это время уже прошло"
	case t.After(now.AddDate(1, 0, 0)):
		errs["starts_at"] = "Встречу можно назначить не дальше чем на год вперёд"
	}
	f.startsAt = t

	if c := strings.TrimSpace(in.Capacity); c != "" {
		if f.capacity, err = strconv.Atoi(c); err != nil || f.capacity < 2 || f.capacity > MaxCapacity {
			errs["capacity"] = fmt.Sprintf("Мест — от 2 до %d, или оставьте пустым", MaxCapacity)
		}
	}
	f.visibility = Visibility(in.Visibility)
	if f.visibility.Label() == "" {
		errs["visibility"] = "Выберите, кому видна встреча"
	}
	return f, errs
}

var (
	ErrNotFound     = errors.New("встреча не найдена")
	ErrForbidden    = errors.New("нет прав на эту встречу")
	ErrNotEditable  = errors.New("отменённая или прошедшая встреча не меняется")
	ErrBadStatus    = errors.New("такой смены статуса нет")
	ErrStaleStatus  = errors.New("статус встречи уже изменился, обновите страницу")
	ErrInvalidInput = errors.New("неверные данные встречи")
)

type Service struct {
	DB  *pgxpool.Pool
	Now func() time.Time
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
		// мест стало больше — отдаём их очереди
		m.Capacity = f.capacity
		return fillSeats(ctx, tx, m, now)
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
		_, err = tx.Exec(ctx, `UPDATE meetups SET status = $2, updated_at = now() WHERE id = $1`, id, to)
		return err
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
