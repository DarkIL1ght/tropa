package meetups

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"tropa/internal/auth"
	"tropa/internal/store/storetest"
)

// fixedNow — 1 марта 2027, 10:00 по Алматы.
var fixedNow = time.Date(2027, 3, 1, 10, 0, 0, 0, Almaty)

func validInput() Input {
	return Input{Title: "Каток на Медеу", Date: "2027-03-06", Time: "18:30", Capacity: "10", Visibility: "public"}
}

func TestValidate(t *testing.T) {
	cases := []struct {
		name string
		edit func(*Input)
		bad  string // поле с ошибкой; пусто — без ошибок
	}{
		{"ок", func(*Input) {}, ""},
		{"без лимита", func(in *Input) { in.Capacity = " " }, ""},
		{"для друзей", func(in *Input) { in.Visibility = "friends" }, ""},
		{"короткое название", func(in *Input) { in.Title = "Ок" }, "title"},
		{"в прошлом", func(in *Input) { in.Date, in.Time = "2027-03-01", "09:59" }, "starts_at"},
		{"прямо сейчас", func(in *Input) { in.Date, in.Time = "2027-03-01", "10:00" }, "starts_at"},
		{"через два года", func(in *Input) { in.Date = "2029-03-06" }, "starts_at"},
		{"нет времени", func(in *Input) { in.Time = "" }, "starts_at"},
		{"1 место", func(in *Input) { in.Capacity = "1" }, "capacity"},
		{"слишком много мест", func(in *Input) { in.Capacity = "201" }, "capacity"},
		{"мест не число", func(in *Input) { in.Capacity = "много" }, "capacity"},
		{"неизвестная видимость", func(in *Input) { in.Visibility = "secret" }, "visibility"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := validInput()
			c.edit(&in)
			f, errs := in.Validate(fixedNow)
			if c.bad == "" && len(errs) > 0 {
				t.Fatalf("неожиданные ошибки: %v", errs)
			}
			if c.bad != "" && (errs[c.bad] == "" || len(errs) != 1) {
				t.Fatalf("ждали ошибку только в %s, получили %v", c.bad, errs)
			}
			if c.name == "ок" && (!f.startsAt.Equal(time.Date(2027, 3, 6, 18, 30, 0, 0, Almaty)) || f.capacity != 10) {
				t.Fatalf("разбор: %+v", f)
			}
		})
	}
}

type env struct {
	s     *Service
	db    *pgxpool.Pool
	auth  *auth.Service
	place int64
	now   *time.Time
}

func setup(t *testing.T) *env {
	t.Helper()
	db := storetest.New(t)
	e := &env{db: db, auth: auth.New(db), s: New(db)}
	now := fixedNow
	e.now = &now
	e.s.Now = func() time.Time { return *e.now }
	err := db.QueryRow(context.Background(),
		`INSERT INTO places (slug, name, category, kind, lat, lon, area)
		 VALUES ('medeu', 'Медеу', 'active', 'rink', 43.157, 77.059, 'almaty') RETURNING id`).Scan(&e.place)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

var userSeq int

func (e *env) user(t *testing.T) *auth.User {
	t.Helper()
	userSeq++
	u, err := e.auth.Register(context.Background(), fmt.Sprintf("u%d@tropa.kz", userSeq), fmt.Sprintf("Участник %d", userSeq), "password123")
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func (e *env) create(t *testing.T, org *auth.User, edit func(*Input)) *Meetup {
	t.Helper()
	in := validInput()
	if edit != nil {
		edit(&in)
	}
	m, errs, err := e.s.Create(context.Background(), org, e.place, in)
	if err != nil {
		t.Fatalf("создание: %v %v", err, errs)
	}
	return m
}

// queue — статусы участников и позиции очереди; проверяет, что позиции идут подряд с 1.
func (e *env) queue(t *testing.T, id int64) (confirmed, offered int, waitlist []int64) {
	t.Helper()
	ps, err := e.s.Participants(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range ps {
		switch p.Status {
		case Confirmed:
			confirmed++
		case Offered:
			offered++
		case Waitlist:
			waitlist = append(waitlist, p.UserID)
			if p.Position != len(waitlist) {
				t.Fatalf("дыра в очереди: позиция %d на %d-м месте", p.Position, len(waitlist))
			}
		}
	}
	return
}

func TestCreateUpdateStatus(t *testing.T) {
	ctx := context.Background()
	e := setup(t)
	org, other := e.user(t), e.user(t)

	if _, errs, err := e.s.Create(ctx, org, e.place, Input{}); !errors.Is(err, ErrInvalidInput) || len(errs) == 0 {
		t.Fatalf("пустая форма: %v %v", err, errs)
	}
	m := e.create(t, org, nil)
	if m.Status != Open || m.Going != 1 || m.Free() != 9 || m.PlaceName != "Медеу" || len(m.Token) != 22 {
		t.Fatalf("новая встреча: %+v", m)
	}
	if got, err := e.s.ByToken(ctx, m.Token); err != nil || got.ID != m.ID {
		t.Fatalf("по токену: %v", err)
	}
	for _, bad := range []string{"", "x", m.Token[:21] + "A", m.Token + "A"} {
		if bad == m.Token {
			continue
		}
		if _, err := e.s.ByToken(ctx, bad); !errors.Is(err, ErrNotFound) {
			t.Fatalf("токен %q: %v", bad, err)
		}
	}

	in := validInput()
	in.Title = "Каток и глинтвейн"
	if _, err := e.s.Update(ctx, other, m.ID, in); !errors.Is(err, ErrForbidden) {
		t.Fatalf("чужая правка: %v", err)
	}
	if _, err := e.s.Update(ctx, org, m.ID, in); err != nil {
		t.Fatal(err)
	}

	// меньше мест, чем записано, — нельзя
	for range 3 {
		if _, err := e.s.Join(ctx, e.user(t), m.ID); err != nil {
			t.Fatal(err)
		}
	}
	in.Capacity = "3"
	if errs, err := e.s.Update(ctx, org, m.ID, in); !errors.Is(err, ErrInvalidInput) || errs["capacity"] == "" {
		t.Fatalf("мест меньше записанных: %v %v", err, errs)
	}
	in.Capacity = "4"
	if _, err := e.s.Update(ctx, org, m.ID, in); err != nil {
		t.Fatal(err)
	}

	if err := e.s.SetStatus(ctx, other, m.ID, Open, Closed); !errors.Is(err, ErrForbidden) {
		t.Fatalf("чужая смена статуса: %v", err)
	}
	if err := e.s.SetStatus(ctx, org, m.ID, Open, Closed); err != nil {
		t.Fatal(err)
	}
	if err := e.s.SetStatus(ctx, org, m.ID, Open, Cancelled); !errors.Is(err, ErrStaleStatus) {
		t.Fatalf("устаревший статус: %v", err)
	}
	if _, err := e.s.Join(ctx, e.user(t), m.ID); !errors.Is(err, ErrClosed) {
		t.Fatalf("запись на закрытую: %v", err)
	}
	if err := e.s.SetStatus(ctx, org, m.ID, Closed, Status("done")); !errors.Is(err, ErrBadStatus) {
		t.Fatalf("несуществующий переход: %v", err)
	}
	if err := e.s.SetStatus(ctx, org, m.ID, Closed, Cancelled); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.Update(ctx, org, m.ID, in); !errors.Is(err, ErrNotEditable) {
		t.Fatalf("правка отменённой: %v", err)
	}

	// прошедшая встреча не меняется и не принимает записи
	m2 := e.create(t, org, nil)
	*e.now = m2.StartsAt.Add(time.Minute)
	if _, err := e.s.Join(ctx, other, m2.ID); !errors.Is(err, ErrClosed) {
		t.Fatalf("запись на прошедшую: %v", err)
	}
	if err := e.s.SetStatus(ctx, org, m2.ID, Open, Closed); !errors.Is(err, ErrNotEditable) {
		t.Fatalf("статус прошедшей: %v", err)
	}
}

// TestConcurrentJoin — 50 человек одновременно записываются на 10 мест.
func TestConcurrentJoin(t *testing.T) {
	ctx := context.Background()
	e := setup(t)
	org := e.user(t)
	m := e.create(t, org, func(in *Input) { in.Capacity = "10" }) // организатор занимает одно место
	users := make([]*auth.User, 50)
	for i := range users {
		users[i] = e.user(t)
	}

	var wg sync.WaitGroup
	errs := make(chan error, len(users))
	for _, u := range users {
		wg.Go(func() {
			if _, err := e.s.Join(ctx, u, m.ID); err != nil {
				errs <- err
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	confirmed, offered, waitlist := e.queue(t, m.ID)
	if confirmed != 10 || offered != 0 || len(waitlist) != 41 {
		t.Fatalf("идут %d, предложено %d, в очереди %d; ждали 10 / 0 / 41", confirmed, offered, len(waitlist))
	}

	// повторная запись ничего не меняет
	if st, err := e.s.Join(ctx, users[0], m.ID); err != nil || st == "" {
		t.Fatalf("повтор: %v %v", st, err)
	}
	if c, _, w := e.queue(t, m.ID); c != 10 || len(w) != 41 {
		t.Fatalf("повтор изменил запись: %d / %d", c, len(w))
	}
}

func TestLeaveOfferExpire(t *testing.T) {
	ctx := context.Background()
	e := setup(t)
	org := e.user(t)
	m := e.create(t, org, func(in *Input) { in.Capacity = "2" })
	a, b, c, d := e.user(t), e.user(t), e.user(t), e.user(t)
	join := func(u *auth.User, want PStatus) {
		t.Helper()
		if st, err := e.s.Join(ctx, u, m.ID); err != nil || st != want {
			t.Fatalf("запись: %v %v, ждали %v", st, err, want)
		}
	}
	join(a, Confirmed)
	join(b, Waitlist)
	join(c, Waitlist)
	join(d, Waitlist)

	if err := e.s.Leave(ctx, org, m.ID); !errors.Is(err, ErrOrganizer) {
		t.Fatalf("организатор вышел: %v", err)
	}
	// выход из середины очереди — без дыр
	if err := e.s.Leave(ctx, c, m.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, w := e.queue(t, m.ID); len(w) != 2 || w[0] != b.ID || w[1] != d.ID {
		t.Fatalf("очередь после выхода c: %v", w)
	}

	// a ушёл — место предложено b, очередь сдвинулась
	if err := e.s.Leave(ctx, a, m.ID); err != nil {
		t.Fatal(err)
	}
	p, err := e.s.Participant(ctx, m.ID, b.ID)
	if err != nil || p.Status != Offered || !p.OfferExpiresAt.Equal(fixedNow.Add(OfferTTL)) {
		t.Fatalf("b: %+v %v", p, err)
	}
	if _, _, w := e.queue(t, m.ID); len(w) != 1 || w[0] != d.ID {
		t.Fatalf("очередь: %v", w)
	}
	// предложенное место держится: новый — в очередь
	nw := e.user(t)
	join(nw, Waitlist)

	// d не может принять чужое предложение
	if err := e.s.Accept(ctx, d, m.ID); !errors.Is(err, ErrNoOffer) {
		t.Fatalf("d принял: %v", err)
	}

	// b не успел — через сутки место уходит d
	*e.now = fixedNow.Add(OfferTTL + time.Second)
	if err := e.s.Accept(ctx, b, m.ID); !errors.Is(err, ErrNoOffer) {
		t.Fatalf("просроченное предложение принято: %v", err)
	}
	n, err := e.s.ExpireOffers(ctx)
	if err != nil || n != 1 {
		t.Fatalf("снято предложений: %d %v", n, err)
	}
	if _, err := e.s.Participant(ctx, m.ID, b.ID); !errors.Is(err, ErrNotJoined) {
		t.Fatalf("b остался: %v", err)
	}
	if p, _ := e.s.Participant(ctx, m.ID, d.ID); p == nil || p.Status != Offered {
		t.Fatalf("d: %+v", p)
	}
	if err := e.s.Accept(ctx, d, m.ID); err != nil {
		t.Fatal(err)
	}
	conf, off, w := e.queue(t, m.ID)
	if conf != 2 || off != 0 || len(w) != 1 || w[0] != nw.ID {
		t.Fatalf("итог: идут %d, предложено %d, очередь %v", conf, off, w)
	}
	if n, _ := e.s.ExpireOffers(ctx); n != 0 {
		t.Fatalf("лишнее снятие: %d", n)
	}

	// больше мест — очередь получает предложение
	in := FromMeetup(m)
	in.Capacity = ""
	if _, err := e.s.Update(ctx, org, m.ID, in); err != nil {
		t.Fatal(err)
	}
	if p, _ := e.s.Participant(ctx, m.ID, nw.ID); p == nil || p.Status != Offered {
		t.Fatalf("после снятия лимита: %+v", p)
	}
}

func TestListsAndVisibility(t *testing.T) {
	ctx := context.Background()
	e := setup(t)
	org, guest := e.user(t), e.user(t)
	pub := e.create(t, org, func(in *Input) { in.Title = "Открытый каток" })
	friends := e.create(t, org, func(in *Input) { in.Title = "Каток для своих"; in.Visibility = "friends" })
	cancelled := e.create(t, org, func(in *Input) { in.Title = "Отменённый каток" })
	if err := e.s.SetStatus(ctx, org, cancelled.ID, Open, Cancelled); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.Join(ctx, guest, friends.ID); err != nil {
		t.Fatal(err)
	}

	page, err := e.s.List(ctx, Filter{Page: 1})
	if err != nil || page.Total != 1 || page.Meetups[0].ID != pub.ID {
		t.Fatalf("список: %+v %v", page, err)
	}
	if page, _ := e.s.List(ctx, Filter{Query: "медеу", Page: 1}); page.Total != 1 {
		t.Fatalf("поиск по месту: %d", page.Total)
	}
	if page, _ := e.s.List(ctx, Filter{Query: "50%", Page: 1}); page.Total != 0 {
		t.Fatalf("LIKE не экранирован: %d", page.Total)
	}
	if page, _ := e.s.List(ctx, Filter{Category: "nature", Page: 1}); page.Total != 0 {
		t.Fatalf("фильтр по группе: %d", page.Total)
	}
	counts, err := e.s.CountByPlace(ctx, []int64{e.place, 999})
	if err != nil || counts[e.place] != 1 || len(counts) != 1 {
		t.Fatalf("счётчик на карте: %v %v", counts, err)
	}
	if list, _ := e.s.ByPlace(ctx, e.place); len(list) != 1 || list[0].ID != pub.ID {
		t.Fatalf("встречи места: %v", list)
	}
	if list, _ := e.s.ByOrganizer(ctx, org.ID); len(list) != 3 {
		t.Fatalf("мои встречи: %d", len(list))
	}
	joined, err := e.s.ByParticipant(ctx, guest.ID)
	if err != nil || len(joined) != 1 || joined[0].ID != friends.ID || joined[0].Me.Status != Confirmed {
		t.Fatalf("я участвую: %+v %v", joined, err)
	}

	// прошедшие пропадают из списка и с карты
	*e.now = pub.StartsAt
	if page, _ := e.s.List(ctx, Filter{Page: 1}); page.Total != 0 {
		t.Fatalf("прошедшая в списке: %d", page.Total)
	}
}

func TestWhen(t *testing.T) {
	at := func(y, m, d, h, min int) time.Time { return time.Date(y, time.Month(m), d, h, min, 0, 0, Almaty) }
	year := time.Now().In(Almaty).Year()
	for _, c := range []struct {
		got, want string
	}{
		{When(at(2027, 3, 6, 18, 30)), "сб, 6 марта 2027, 18:30"},
		{When(at(year, 1, 1, 9, 5)), weekdaysShort[at(year, 1, 1, 9, 5).Weekday()] + ", 1 января, 09:05"}, // текущий год — без года
		{When(time.Date(2027, 3, 6, 13, 30, 0, 0, time.UTC)), "сб, 6 марта 2027, 18:30"},                  // UTC → Алматы
		{Clock(at(2027, 12, 31, 23, 59)), "31 декабря, 23:59"},
	} {
		if c.got != c.want {
			t.Errorf("%q, ждали %q", c.got, c.want)
		}
	}
}

// queued — уведомления пользователя в очереди: вид → текст.
func (e *env) queued(t *testing.T, u *auth.User) map[string]string {
	t.Helper()
	rows, err := e.db.Query(context.Background(), `SELECT kind, text FROM notifications WHERE user_id = $1`, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for rows.Next() {
		var k, text string
		rows.Scan(&k, &text)
		out[k] += text
	}
	return out
}

func TestNotifications(t *testing.T) {
	ctx := context.Background()
	e := setup(t)
	e.s.SiteURL = "https://tropa.test"
	org, a, b, offline := e.user(t), e.user(t), e.user(t), e.user(t)
	for i, u := range []*auth.User{org, a, b} {
		if _, err := e.db.Exec(ctx, `UPDATE users SET telegram_chat_id = $2 WHERE id = $1`, u.ID, 100+i); err != nil {
			t.Fatal(err)
		}
	}
	m := e.create(t, org, func(in *Input) { in.Capacity = "2" })
	e.s.Join(ctx, a, m.ID)
	e.s.Join(ctx, b, m.ID)
	e.s.Join(ctx, offline, m.ID)

	// a ушёл — b предложено место, уведомление со ссылкой и сроком
	if err := e.s.Leave(ctx, a, m.ID); err != nil {
		t.Fatal(err)
	}
	offer := e.queued(t, b)["offer"]
	if !strings.Contains(offer, "https://tropa.test/m/"+m.Token) || !strings.Contains(offer, "2 марта, 10:00") {
		t.Fatalf("уведомление о месте: %q", offer)
	}

	// за 2 часа до конца предложения — напоминание, один раз
	*e.now = fixedNow.Add(OfferTTL - time.Hour)
	e.s.Remind(ctx)
	e.s.Remind(ctx)
	if q := e.queued(t, b); q["offer_soon"] == "" || strings.Count(q["offer_soon"], "держится") != 1 {
		t.Fatalf("напоминание о предложении: %q", q["offer_soon"])
	}
	e.s.Accept(ctx, b, m.ID)

	// перенос — всем участникам, кроме организатора; без Telegram — не ставится
	in := FromMeetup(m)
	in.Time = "20:00"
	if _, err := e.s.Update(ctx, org, m.ID, in); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(e.queued(t, b)["moved"], "20:00") || e.queued(t, org)["moved"] != "" || len(e.queued(t, offline)) != 0 {
		t.Fatal("уведомления о переносе")
	}

	// за сутки — напоминание тем, кто идёт
	*e.now = time.Date(2027, 3, 5, 21, 0, 0, 0, Almaty)
	e.s.Remind(ctx)
	if !strings.Contains(e.queued(t, b)["reminder"], "завтра") || e.queued(t, org)["reminder"] != "" {
		t.Fatal("напоминание о встрече")
	}

	if err := e.s.SetStatus(ctx, org, m.ID, Open, Cancelled); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(e.queued(t, b)["cancelled"], "отменили") {
		t.Fatal("уведомление об отмене")
	}
}
