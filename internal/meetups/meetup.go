// Package meetups — встречи на местах карты: создание, правка, статусы,
// запись с листом ожидания, списки.
package meetups

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
	"tropa/internal/auth"
	"unicode/utf8"
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

var visibilityLabels = map[Visibility]string{Public: "Открытая — видна всем на карте", Friends: "Для друзей — видят ваши друзья в Tropa и все, у кого есть ссылка"}

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
