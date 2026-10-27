// Package trips — походы: создание, правка, статусы, каталог.
package trips

import (
	"context"
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

// Almaty — пояс, в котором считаются «сегодня» и даты походов.
var Almaty = mustLoad("Asia/Almaty")

func mustLoad(name string) *time.Location {
	loc, err := time.LoadLocation(name)
	if err != nil {
		panic(err)
	}
	return loc
}

const DateLayout = "2006-01-02"

type Status string

const (
	Draft     Status = "draft"
	Open      Status = "open"
	Full      Status = "full" // ставит и снимает запись (этап 3), не организатор
	Closed    Status = "closed"
	Done      Status = "done"
	Cancelled Status = "cancelled"
)

var statusLabels = map[Status]string{
	Draft: "Черновик", Open: "Идёт запись", Full: "Мест нет",
	Closed: "Запись закрыта", Done: "Прошёл", Cancelled: "Отменён",
}

func (s Status) Label() string { return statusLabels[s] }

// Public — виден ли поход в каталоге и по ссылке посторонним.
func (s Status) Public() bool { return s == Open || s == Full || s == Closed || s == Done }

// Editable — можно ли править поля похода.
func (s Status) Editable() bool { return s != Done && s != Cancelled }

// transitions — переходы, которые организатор делает вручную.
var transitions = map[Status][]Status{
	Draft:  {Open, Cancelled},
	Open:   {Closed, Cancelled},
	Full:   {Closed, Cancelled},
	Closed: {Open, Done, Cancelled},
}

// Next — куда организатор может перевести поход из статуса s.
func (s Status) Next() []Status { return transitions[s] }

func (s Status) CanBecome(to Status) bool { return slices.Contains(transitions[s], to) }

// actionLabels — подписи кнопок перехода.
var actionLabels = map[Status]string{
	Open: "Открыть запись", Closed: "Закрыть запись", Done: "Отметить как прошедший", Cancelled: "Отменить поход",
}

func (s Status) Action() string { return actionLabels[s] }

type Difficulty string

var Difficulties = []Difficulty{"easy", "medium", "hard"}

var difficultyLabels = map[Difficulty]string{"easy": "Лёгкий", "medium": "Средний", "hard": "Сложный"}

func (d Difficulty) Label() string { return difficultyLabels[d] }

type Trip struct {
	ID            int64
	OrganizerID   int64
	OrganizerName string
	Title         string
	Description   string
	Region        string
	MeetingPoint  string
	StartDate     time.Time
	EndDate       time.Time
	Difficulty    Difficulty
	Capacity      int
	Fee           int
	Status        Status
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// Days — длительность похода в днях, включая оба края.
func (t *Trip) Days() int { return int(t.EndDate.Sub(t.StartDate).Hours()/24) + 1 }

// CanManage — организатор похода или админ.
func (t *Trip) CanManage(u *auth.User) bool {
	return u != nil && (u.ID == t.OrganizerID || u.IsAdmin())
}

// VisibleTo — черновики и отменённые видны только тем, кто управляет походом.
func (t *Trip) VisibleTo(u *auth.User) bool { return t.Status.Public() || t.CanManage(u) }

// Input — поля формы похода в виде строк, как пришли из запроса.
type Input struct {
	Title, Description, Region, MeetingPoint string
	StartDate, EndDate                       string
	Difficulty                               string
	Capacity, Fee                            string
}

// FromTrip заполняет форму значениями существующего похода.
func FromTrip(t *Trip) Input {
	return Input{
		Title: t.Title, Description: t.Description, Region: t.Region, MeetingPoint: t.MeetingPoint,
		StartDate: t.StartDate.Format(DateLayout), EndDate: t.EndDate.Format(DateLayout),
		Difficulty: string(t.Difficulty), Capacity: strconv.Itoa(t.Capacity), Fee: strconv.Itoa(t.Fee),
	}
}

// fields — проверенные значения формы.
type fields struct {
	title, description, region, meetingPoint string
	start, end                               time.Time
	difficulty                               Difficulty
	capacity, fee                            int
}

// Validate проверяет форму. today — текущая дата в Алматы: новый поход
// не может начинаться в прошлом (для правки прошлое не проверяется).
func (in Input) Validate(today time.Time, isNew bool) (fields, map[string]string) {
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
	f.region = strings.TrimSpace(in.Region)
	if n := utf8.RuneCountInString(f.region); n < 2 || n > 80 {
		errs["region"] = "Укажите регион (до 80 символов)"
	}
	f.meetingPoint = strings.TrimSpace(in.MeetingPoint)
	if utf8.RuneCountInString(f.meetingPoint) > 200 {
		errs["meeting_point"] = "Место сбора — не длиннее 200 символов"
	}

	var err error
	if f.start, err = time.Parse(DateLayout, in.StartDate); err != nil {
		errs["start_date"] = "Укажите дату начала"
	} else if isNew && f.start.Before(today) {
		errs["start_date"] = "Дата начала уже прошла"
	}
	if f.end, err = time.Parse(DateLayout, in.EndDate); err != nil {
		errs["end_date"] = "Укажите дату окончания"
	} else if errs["start_date"] == "" && f.end.Before(f.start) {
		errs["end_date"] = "Окончание раньше начала"
	} else if errs["start_date"] == "" && f.end.Sub(f.start) > 60*24*time.Hour {
		errs["end_date"] = "Поход — не дольше 60 дней"
	}

	f.difficulty = Difficulty(in.Difficulty)
	if f.difficulty.Label() == "" {
		errs["difficulty"] = "Выберите сложность"
	}
	if f.capacity, err = strconv.Atoi(strings.TrimSpace(in.Capacity)); err != nil || f.capacity < 1 || f.capacity > 500 {
		errs["capacity"] = "Мест — от 1 до 500"
	}
	fee := strings.TrimSpace(in.Fee)
	if fee == "" {
		fee = "0"
	}
	if f.fee, err = strconv.Atoi(fee); err != nil || f.fee < 0 || f.fee > 10_000_000 {
		errs["fee"] = "Взнос — целое число тенге, 0 если бесплатно"
	}
	return f, errs
}

var (
	ErrNotFound     = errors.New("поход не найден")
	ErrForbidden    = errors.New("нет прав на этот поход")
	ErrNotEditable  = errors.New("прошедший или отменённый поход не редактируется")
	ErrBadStatus    = errors.New("такой смены статуса нет")
	ErrStaleStatus  = errors.New("статус похода уже изменился, обновите страницу")
	ErrInvalidInput = errors.New("неверные данные похода")
)

type Service struct {
	DB  *pgxpool.Pool
	Now func() time.Time
}

func New(db *pgxpool.Pool) *Service { return &Service{DB: db, Now: time.Now} }

// Today — сегодняшняя дата в Алматы (полночь, UTC — как даты из базы).
func (s *Service) Today() time.Time {
	y, m, d := s.Now().In(Almaty).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// Create сохраняет новый поход черновиком. При ошибках проверки
// возвращает ErrInvalidInput и ошибки по полям.
func (s *Service) Create(ctx context.Context, organizer *auth.User, in Input) (int64, map[string]string, error) {
	f, errs := in.Validate(s.Today(), true)
	if len(errs) > 0 {
		return 0, errs, ErrInvalidInput
	}
	var id int64
	err := s.DB.QueryRow(ctx,
		`INSERT INTO trips (organizer_id, title, description, region, meeting_point,
		                    start_date, end_date, difficulty, capacity, fee)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10) RETURNING id`,
		organizer.ID, f.title, f.description, f.region, f.meetingPoint,
		f.start, f.end, f.difficulty, f.capacity, f.fee).Scan(&id)
	if err != nil {
		return 0, nil, fmt.Errorf("создание похода: %w", err)
	}
	return id, nil, nil
}

// Update меняет поля похода. Права и статус проверяются в той же
// транзакции под блокировкой строки, чтобы не разойтись со сменой статуса.
func (s *Service) Update(ctx context.Context, u *auth.User, id int64, in Input) (map[string]string, error) {
	f, errs := in.Validate(s.Today(), false)
	if len(errs) > 0 {
		return errs, ErrInvalidInput
	}
	return nil, pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		t, err := getTrip(ctx, tx, id, true)
		if err != nil {
			return err
		}
		if !t.CanManage(u) {
			return ErrForbidden
		}
		if !t.Status.Editable() {
			return ErrNotEditable
		}
		// TODO(этап 3): не давать уменьшить capacity ниже числа подтверждённых участников.
		_, err = tx.Exec(ctx,
			`UPDATE trips SET title = $2, description = $3, region = $4, meeting_point = $5,
			        start_date = $6, end_date = $7, difficulty = $8, capacity = $9, fee = $10,
			        updated_at = now()
			  WHERE id = $1`,
			id, f.title, f.description, f.region, f.meetingPoint,
			f.start, f.end, f.difficulty, f.capacity, f.fee)
		return err
	})
}

// SetStatus переводит поход из from в to. from — статус, который видел
// организатор: если поход успел смениться, вернётся ErrStaleStatus.
func (s *Service) SetStatus(ctx context.Context, u *auth.User, id int64, from, to Status) error {
	if !from.CanBecome(to) {
		return ErrBadStatus
	}
	return pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		t, err := getTrip(ctx, tx, id, true)
		if err != nil {
			return err
		}
		if !t.CanManage(u) {
			return ErrForbidden
		}
		if t.Status != from {
			return ErrStaleStatus
		}
		_, err = tx.Exec(ctx, `UPDATE trips SET status = $2, updated_at = now() WHERE id = $1`, id, to)
		return err
	})
}

// Get возвращает поход по id (без проверки видимости — её делает вызывающий).
func (s *Service) Get(ctx context.Context, id int64) (*Trip, error) {
	return getTrip(ctx, s.DB, id, false)
}

type querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

const tripColumns = `t.id, t.organizer_id, u.name, t.title, t.description, t.region, t.meeting_point,
	t.start_date, t.end_date, t.difficulty, t.capacity, t.fee, t.status, t.created_at, t.updated_at`

func scanTrip(row pgx.Row) (*Trip, error) {
	t := &Trip{}
	err := row.Scan(&t.ID, &t.OrganizerID, &t.OrganizerName, &t.Title, &t.Description, &t.Region,
		&t.MeetingPoint, &t.StartDate, &t.EndDate, &t.Difficulty, &t.Capacity, &t.Fee, &t.Status,
		&t.CreatedAt, &t.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return t, err
}

func getTrip(ctx context.Context, q querier, id int64, forUpdate bool) (*Trip, error) {
	sql := `SELECT ` + tripColumns + ` FROM trips t JOIN users u ON u.id = t.organizer_id WHERE t.id = $1`
	if forUpdate {
		sql += ` FOR UPDATE OF t`
	}
	return scanTrip(q.QueryRow(ctx, sql, id))
}
