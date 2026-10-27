package trips

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"testing"
	"time"

	"tropa/internal/auth"
	"tropa/internal/store/storetest"
)

// fixedNow — 1 марта 2027, 10:00 по Алматы.
var fixedNow = time.Date(2027, 3, 1, 10, 0, 0, 0, Almaty)

func validInput() Input {
	return Input{
		Title: "Кольсайские озёра", Region: "Алматинская область",
		StartDate: "2027-03-10", EndDate: "2027-03-12",
		Difficulty: "medium", Capacity: "12", Fee: "15000",
	}
}

func TestValidate(t *testing.T) {
	today := time.Date(2027, 3, 1, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		name  string
		edit  func(*Input)
		isNew bool
		bad   string // поле с ошибкой; пусто — без ошибок
	}{
		{"ок", func(*Input) {}, true, ""},
		{"пустой взнос = бесплатно", func(in *Input) { in.Fee = "" }, true, ""},
		{"однодневный", func(in *Input) { in.EndDate = in.StartDate }, true, ""},
		{"короткое название", func(in *Input) { in.Title = "Ок" }, true, "title"},
		{"нет региона", func(in *Input) { in.Region = " " }, true, "region"},
		{"начало в прошлом", func(in *Input) { in.StartDate = "2027-02-28" }, true, "start_date"},
		{"прошлое при правке можно", func(in *Input) { in.StartDate = "2027-02-28" }, false, ""},
		{"конец раньше начала", func(in *Input) { in.EndDate = "2027-03-09" }, true, "end_date"},
		{"дольше 60 дней", func(in *Input) { in.EndDate = "2027-06-01" }, true, "end_date"},
		{"неверная дата", func(in *Input) { in.StartDate = "10.03.2027" }, true, "start_date"},
		{"неизвестная сложность", func(in *Input) { in.Difficulty = "extreme" }, true, "difficulty"},
		{"0 мест", func(in *Input) { in.Capacity = "0" }, true, "capacity"},
		{"мест не число", func(in *Input) { in.Capacity = "много" }, true, "capacity"},
		{"отрицательный взнос", func(in *Input) { in.Fee = "-1" }, true, "fee"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := validInput()
			c.edit(&in)
			_, errs := in.Validate(today, c.isNew)
			if c.bad == "" && len(errs) > 0 {
				t.Fatalf("неожиданные ошибки: %v", errs)
			}
			if c.bad != "" && (errs[c.bad] == "" || len(errs) != 1) {
				t.Fatalf("ждали ошибку только в %s, получили %v", c.bad, errs)
			}
		})
	}
}

func TestTransitions(t *testing.T) {
	allowed := map[[2]Status]bool{
		{Draft, Open}: true, {Draft, Cancelled}: true,
		{Open, Closed}: true, {Open, Cancelled}: true,
		{Full, Closed}: true, {Full, Cancelled}: true,
		{Closed, Open}: true, {Closed, Done}: true, {Closed, Cancelled}: true,
	}
	all := []Status{Draft, Open, Full, Closed, Done, Cancelled}
	for _, from := range all {
		for _, to := range all {
			if got := from.CanBecome(to); got != allowed[[2]Status{from, to}] {
				t.Errorf("%s → %s: %v", from, to, got)
			}
		}
	}
	// «Мест нет» ставит только запись — вручную в full перейти нельзя
	for _, from := range all {
		if from.CanBecome(Full) {
			t.Errorf("ручной переход %s → full", from)
		}
	}
}

func TestParseFilterRoundTrip(t *testing.T) {
	q, _ := url.ParseQuery("q=озёра&region=Алматинская+область&difficulty=hard&from=2027-03-01&to=2027-04-01&past=1&page=3")
	f := ParseFilter(q)
	if f.Query != "озёра" || f.Difficulty != "hard" || !f.Past || f.Page != 3 || f.From.Day() != 1 || f.To.Month() != 4 {
		t.Fatalf("разбор: %+v", f)
	}
	again := ParseFilter(mustQuery(t, f.PageURL(3)))
	if again != f {
		t.Fatalf("после PageURL фильтр изменился:\n%+v\n%+v", f, again)
	}

	junk := ParseFilter(url.Values{"difficulty": {"x"}, "from": {"вчера"}, "page": {"-5"}})
	if junk.Difficulty != "" || !junk.From.IsZero() || junk.Page != 1 {
		t.Fatalf("мусор не отброшен: %+v", junk)
	}
	if got := (Filter{Page: 1}).PageURL(1); got != "/trips" {
		t.Fatalf("пустой фильтр: %s", got)
	}
}

func mustQuery(t *testing.T, u string) url.Values {
	parsed, err := url.Parse(u)
	if err != nil {
		t.Fatal(err)
	}
	return parsed.Query()
}

// setup — сервис с фиксированным «сейчас» и два пользователя.
func setup(t *testing.T) (*Service, *auth.User, *auth.User) {
	db := storetest.New(t)
	a := auth.New(db)
	org, err := a.Register(context.Background(), "org@tropa.kz", "Организатор", "password123")
	if err != nil {
		t.Fatal(err)
	}
	other, err := a.Register(context.Background(), "other@tropa.kz", "Участник", "password123")
	if err != nil {
		t.Fatal(err)
	}
	s := New(db)
	s.Now = func() time.Time { return fixedNow }
	return s, org, other
}

func create(t *testing.T, s *Service, u *auth.User, edit func(*Input)) int64 {
	t.Helper()
	in := validInput()
	if edit != nil {
		edit(&in)
	}
	id, errs, err := s.Create(context.Background(), u, in)
	if err != nil {
		t.Fatalf("создание: %v %v", err, errs)
	}
	return id
}

func TestCreateUpdateStatus(t *testing.T) {
	ctx := context.Background()
	s, org, other := setup(t)

	if _, errs, err := s.Create(ctx, org, Input{}); !errors.Is(err, ErrInvalidInput) || len(errs) == 0 {
		t.Fatalf("пустая форма: %v %v", err, errs)
	}

	id := create(t, s, org, nil)
	tr, err := s.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if tr.Status != Draft || tr.OrganizerName != "Организатор" || tr.Fee != 15000 || tr.Days() != 3 {
		t.Fatalf("новый поход: %+v", tr)
	}
	if tr.VisibleTo(other) || tr.VisibleTo(nil) || !tr.VisibleTo(org) {
		t.Fatal("черновик должен видеть только организатор")
	}

	in := validInput()
	in.Title = "Кольсай и Кайынды"
	if _, err := s.Update(ctx, other, id, in); !errors.Is(err, ErrForbidden) {
		t.Fatalf("чужая правка: %v", err)
	}
	if _, err := s.Update(ctx, org, id, in); err != nil {
		t.Fatal(err)
	}
	if tr, _ = s.Get(ctx, id); tr.Title != "Кольсай и Кайынды" {
		t.Fatalf("правка не сохранилась: %s", tr.Title)
	}

	if err := s.SetStatus(ctx, other, id, Draft, Open); !errors.Is(err, ErrForbidden) {
		t.Fatalf("чужая смена статуса: %v", err)
	}
	if err := s.SetStatus(ctx, org, id, Draft, Full); !errors.Is(err, ErrBadStatus) {
		t.Fatalf("draft → full: %v", err)
	}
	if err := s.SetStatus(ctx, org, id, Draft, Open); err != nil {
		t.Fatal(err)
	}
	// вторая вкладка с устаревшим статусом
	if err := s.SetStatus(ctx, org, id, Draft, Cancelled); !errors.Is(err, ErrStaleStatus) {
		t.Fatalf("устаревший статус: %v", err)
	}
	if tr, _ = s.Get(ctx, id); !tr.VisibleTo(nil) {
		t.Fatal("открытый поход должен быть виден всем")
	}

	for _, step := range [][2]Status{{Open, Closed}, {Closed, Done}} {
		if err := s.SetStatus(ctx, org, id, step[0], step[1]); err != nil {
			t.Fatal(step, err)
		}
	}
	if _, err := s.Update(ctx, org, id, in); !errors.Is(err, ErrNotEditable) {
		t.Fatalf("правка прошедшего: %v", err)
	}
	if _, err := s.Get(ctx, 999_999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("нет похода: %v", err)
	}
}

func TestCatalog(t *testing.T) {
	ctx := context.Background()
	s, org, _ := setup(t)
	open := func(edit func(*Input)) int64 {
		id := create(t, s, org, edit)
		if err := s.SetStatus(ctx, org, id, Draft, Open); err != nil {
			t.Fatal(err)
		}
		return id
	}

	late := open(func(in *Input) { in.Title = "Чарын"; in.Region = "Алматинская область"; in.StartDate, in.EndDate = "2027-05-01", "2027-05-02" })
	early := open(func(in *Input) { in.Title = "Бурабай"; in.Region = "Акмолинская область"; in.Difficulty = "easy" })
	create(t, s, org, func(in *Input) { in.Title = "Черновик" }) // не в каталоге
	weird := open(func(in *Input) { in.Title = "Скидка 50% на Big_Almaty" })

	// закончился: создаём в «прошлом» сдвигом часов
	s.Now = func() time.Time { return fixedNow.AddDate(0, 0, -20) }
	past := open(func(in *Input) { in.Title = "Февральский"; in.StartDate, in.EndDate = "2027-02-12", "2027-02-13" })
	s.Now = func() time.Time { return fixedNow }

	ids := func(f Filter) []int64 {
		t.Helper()
		p, err := s.Catalog(ctx, f)
		if err != nil {
			t.Fatal(err)
		}
		var out []int64
		for _, tr := range p.Trips {
			out = append(out, tr.ID)
		}
		return out
	}
	eq := func(name string, got []int64, want ...int64) {
		t.Helper()
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("%s: %v, ждали %v", name, got, want)
		}
	}

	eq("предстоящие по дате", ids(Filter{}), early, weird, late)
	eq("прошедшие", ids(Filter{Past: true}), past)
	eq("поиск без регистра", ids(Filter{Query: "ЧАРЫН"}), late)
	eq("поиск по региону", ids(Filter{Query: "акмолин"}), early)
	eq("% буквально", ids(Filter{Query: "50%"}), weird)
	eq("_ буквально", ids(Filter{Query: "g_A"}), weird)
	eq("_ не шаблон", ids(Filter{Query: "Bi_"}))
	eq("регион точно", ids(Filter{Region: "алматинская область"}), weird, late)
	eq("сложность", ids(Filter{Difficulty: "easy"}), early)
	eq("с даты", ids(Filter{From: time.Date(2027, 4, 1, 0, 0, 0, 0, time.UTC)}), late)
	eq("по дату", ids(Filter{To: time.Date(2027, 3, 10, 0, 0, 0, 0, time.UTC)}), early, weird)

	regions, err := s.Regions(ctx)
	if err != nil || fmt.Sprint(regions) != "[Акмолинская область Алматинская область]" {
		t.Fatalf("регионы: %v %v", regions, err)
	}
	mine, err := s.ByOrganizer(ctx, org.ID)
	if err != nil || len(mine) != 5 {
		t.Fatalf("мои походы: %d %v", len(mine), err)
	}
}

func TestCatalogPagination(t *testing.T) {
	ctx := context.Background()
	s, org, _ := setup(t)
	for i := range PageSize + 3 {
		id := create(t, s, org, func(in *Input) { in.Title = fmt.Sprintf("Поход %02d", i) })
		if err := s.SetStatus(ctx, org, id, Draft, Open); err != nil {
			t.Fatal(err)
		}
	}
	p1, _ := s.Catalog(ctx, Filter{Page: 1})
	p2, _ := s.Catalog(ctx, Filter{Page: 2})
	p9, _ := s.Catalog(ctx, Filter{Page: 9}) // за концом — последняя страница
	if p1.Total != PageSize+3 || p1.TotalPages != 2 || len(p1.Trips) != PageSize || !p1.HasNext() || p1.HasPrev() {
		t.Fatalf("стр. 1: total=%d pages=%d n=%d", p1.Total, p1.TotalPages, len(p1.Trips))
	}
	if len(p2.Trips) != 3 || p2.HasNext() || !p2.HasPrev() || p9.Page != 2 {
		t.Fatalf("стр. 2: n=%d; стр. 9 → %d", len(p2.Trips), p9.Page)
	}
	if p1.Trips[PageSize-1].ID >= p2.Trips[0].ID {
		t.Fatal("страницы пересекаются или не по порядку")
	}
}
