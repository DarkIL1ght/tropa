package places

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"tropa/internal/store/storetest"
)

func valid() Place {
	return Place{
		Slug: "kok-tobe", Name: "Кок-Тобе", Category: Nature, Kind: "viewpoint",
		Lat: 43.2331, Lon: 76.9755, Area: Almaty,
	}
}

func TestValidate(t *testing.T) {
	cases := []struct {
		name string
		edit func(*Place)
		bad  string
	}{
		{"ок", func(*Place) {}, ""},
		{"область", func(p *Place) { p.Area = Region; p.Lat, p.Lon = 43.325, 79.058 }, ""},
		{"slug с заглавной", func(p *Place) { p.Slug = "Kok-tobe" }, "slug"},
		{"slug с двойным дефисом", func(p *Place) { p.Slug = "kok--tobe" }, "slug"},
		{"slug кириллицей", func(p *Place) { p.Slug = "кок-тобе" }, "slug"},
		{"нет названия", func(p *Place) { p.Name = " " }, "name"},
		{"неизвестная группа", func(p *Place) { p.Category = "food" }, "category"},
		{"вид не из группы", func(p *Place) { p.Kind = "cinema" }, "kind"},
		{"Астана", func(p *Place) { p.Area = Region; p.Lat, p.Lon = 51.13, 71.43 }, "coords"},
		{"нулевые координаты", func(p *Place) { p.Lat, p.Lon = 0, 0 }, "coords"},
		{"almaty, но Чарын", func(p *Place) { p.Lat, p.Lon = 43.325, 79.058 }, "coords"},
		{"неизвестная area", func(p *Place) { p.Area = "city" }, "area"},
		{"сезон", func(p *Place) { p.Season = "spring" }, "season"},
		{"сайт без схемы", func(p *Place) { p.Website = "koktobe.kz" }, "website"},
		{"сайт javascript:", func(p *Place) { p.Website = "javascript:alert(1)" }, "website"},
		{"osm без ссылки", func(p *Place) { p.Source = "osm" }, "osm_ref"},
		{"кривой osm_ref", func(p *Place) { p.OSMRef = "node-123" }, "osm_ref"},
		{"статус", func(p *Place) { p.Status = "deleted" }, "status"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := valid()
			c.edit(&p)
			p.Normalize()
			errs := p.Validate()
			if c.bad == "" && len(errs) > 0 {
				t.Fatalf("ошибки: %v", errs)
			}
			if c.bad != "" && (errs[c.bad] == "" || len(errs) != 1) {
				t.Fatalf("ждали ошибку только в %s, получили %v", c.bad, errs)
			}
		})
	}
}

func TestNormalizeDefaults(t *testing.T) {
	p := valid()
	p.Name = "  Кок-Тобе  "
	p.Normalize()
	if p.Name != "Кок-Тобе" || p.Season != "all_year" || p.Source != "manual" || p.Status != Published {
		t.Fatalf("%+v", p)
	}
}

func TestTwoGISURL(t *testing.T) {
	p := valid()
	got := p.TwoGISURL()
	want := "https://2gis.kz/almaty/search/%D0%9A%D0%BE%D0%BA-%D0%A2%D0%BE%D0%B1%D0%B5?m=76.975500%2C43.233100%2F16"
	if got != want {
		t.Fatalf("\n%s\n%s", got, want)
	}
}

// Настоящие seed-файлы проекта должны проходить проверку.
func TestRepoSeedFilesValid(t *testing.T) {
	list, err := LoadSeed(os.DirFS("../../data/places"))
	if err != nil {
		t.Fatal(err)
	}
	if len(list) < 20 {
		t.Fatalf("в seed всего %d мест", len(list))
	}
}

func TestLoadSeed(t *testing.T) {
	good := `[{"slug":"kok-tobe","name":"Кок-Тобе","category":"nature","kind":"viewpoint","lat":43.2331,"lon":76.9755,"area":"almaty"}]`
	cases := []struct {
		name  string
		files fstest.MapFS
		err   string // часть текста ошибки; пусто — успех
		count int
	}{
		{"ок + candidates пропущен", fstest.MapFS{
			"a.json":          {Data: []byte(good)},
			"candidates.json": {Data: []byte(`мусор`)},
		}, "", 1},
		{"неизвестное поле", fstest.MapFS{
			"a.json": {Data: []byte(`[{"slug":"x1","nmae":"опечатка"}]`)},
		}, "unknown field", 0},
		{"битый json", fstest.MapFS{"a.json": {Data: []byte(`[{`)}}, "a.json", 0},
		{"slug в двух файлах", fstest.MapFS{
			"a.json": {Data: []byte(good)}, "b.json": {Data: []byte(good)},
		}, "slug уже есть в a.json", 0},
		{"ошибка проверки", fstest.MapFS{
			"a.json": {Data: []byte(strings.Replace(good, `"almaty"`, `"moon"`, 1))},
		}, "a.json[0] kok-tobe: area", 0},
		{"osm_ref дважды", fstest.MapFS{
			"a.json": {Data: []byte(`[
				{"slug":"one","name":"Один","category":"nature","kind":"park","lat":43.2,"lon":76.9,"area":"almaty","source":"osm","osm_ref":"way/1"},
				{"slug":"two","name":"Два","category":"nature","kind":"park","lat":43.2,"lon":76.9,"area":"almaty","source":"osm","osm_ref":"way/1"}]`)},
		}, "osm_ref way/1 уже у one", 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			list, err := LoadSeed(c.files)
			if c.err == "" {
				if err != nil || len(list) != c.count {
					t.Fatalf("n=%d err=%v", len(list), err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.err) || list != nil {
				t.Fatalf("ждали ошибку с %q, получили %v (n=%d)", c.err, err, len(list))
			}
		})
	}
}

func TestSeedAndSearch(t *testing.T) {
	ctx := context.Background()
	s := New(storetest.New(t))

	list, err := LoadSeed(os.DirFS("../../data/places"))
	if err != nil {
		t.Fatal(err)
	}
	st, err := s.Seed(ctx, list)
	if err != nil || st.Inserted != len(list) {
		t.Fatalf("первый seed: %v %v", st, err)
	}

	// повторный seed с теми же данными — ничего не меняется
	list, _ = LoadSeed(os.DirFS("../../data/places"))
	if st, err := s.Seed(ctx, list); err != nil || st.Unchanged != len(list) || st.Inserted+st.Updated != 0 {
		t.Fatalf("повторный seed: %v %v", st, err)
	}

	// одно поле поменялось — одно обновление
	list[0].Description += " Обновлено."
	if st, err := s.Seed(ctx, list); err != nil || st.Updated != 1 || st.Unchanged != len(list)-1 {
		t.Fatalf("seed с правкой: %v %v", st, err)
	}

	// плохое место в середине — откатывается всё, включая хорошие до него
	bad := []*Place{
		{Slug: "brand-new", Name: "Новое", Category: Nature, Kind: "park", Lat: 43.2, Lon: 76.9, Area: Almaty},
		{Slug: "broken", Name: "Сломанное", Category: Nature, Kind: "park", Lat: 0, Lon: 0, Area: Almaty},
	}
	if _, err := s.Seed(ctx, bad); !errors.Is(err, ErrInvalid) {
		t.Fatalf("плохой seed: %v", err)
	}
	if _, err := s.Get(ctx, "brand-new"); !errors.Is(err, ErrNotFound) {
		t.Fatal("частичный seed не откатился")
	}

	// скрытое место не видно
	hidden := &Place{Slug: "secret", Name: "Скрытое", Category: Nature, Kind: "park", Lat: 43.2, Lon: 76.9, Area: Almaty, Status: Hidden}
	if _, err := s.Upsert(ctx, hidden); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, "secret"); !errors.Is(err, ErrNotFound) {
		t.Fatal("скрытое место отдаётся")
	}

	p, err := s.Get(ctx, "medeu")
	if err != nil || p.Kind != "rink" || p.OSMRef != "way/171504564" || p.Season != "winter" {
		t.Fatalf("medeu: %+v %v", p, err)
	}

	count := func(q Query) int {
		t.Helper()
		res, err := s.Search(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range res {
			if p.Status != Published {
				t.Fatalf("в выдаче %s со статусом %s", p.Slug, p.Status)
			}
		}
		return len(res)
	}
	published := 0
	for _, p := range list {
		if p.Status == Published {
			published++
		}
	}
	if published == len(list) {
		t.Fatal("в seed нет скрытых и черновиков — проверка статусов ничего не проверяет")
	}
	all := count(Query{})
	city := count(Query{BBox: &CityBBox})
	if all != published || city == 0 || city >= all {
		t.Fatalf("всего %d, в городе %d, опубликовано в seed %d", all, city, published)
	}
	for _, c := range []struct {
		name string
		q    Query
		test func(*Place) bool
	}{
		{"в тепле", Query{IndoorOnly: true}, func(p *Place) bool { return p.Indoor }},
		{"бесплатно", Query{FreeOnly: true}, func(p *Place) bool { return p.Free }},
		{"культура", Query{Categories: []Category{Culture}}, func(p *Place) bool { return p.Category == Culture }},
		{"две группы", Query{Categories: []Category{Active, Culture}}, func(p *Place) bool { return p.Category == Active || p.Category == Culture }},
		{"рамка", Query{BBox: &BBox{South: 43.10, West: 77.0, North: 43.20, East: 77.2}}, func(p *Place) bool {
			return p.Lat >= 43.10 && p.Lat <= 43.20 && p.Lon >= 77.0 && p.Lon <= 77.2
		}},
	} {
		res, err := s.Search(ctx, c.q)
		if err != nil || len(res) == 0 {
			t.Fatalf("%s: пусто или ошибка %v", c.name, err)
		}
		for _, p := range res {
			if !c.test(p) {
				t.Errorf("%s: лишнее место %s", c.name, p.Slug)
			}
		}
	}
	if n := count(Query{Text: "медеу"}); n != 1 {
		t.Errorf("поиск «медеу»: %d", n)
	}
	if n := count(Query{Text: "%"}); n != 0 {
		t.Errorf("поиск «%%» должен искать буквально: %d", n)
	}
	if n := count(Query{Limit: 3}); n != 3 {
		t.Errorf("limit: %d", n)
	}
}

func TestBoundaries(t *testing.T) {
	for name, c := range map[string]struct {
		lat, lon float64
		want     Area
	}{
		"центр Алматы": {43.24, 76.92, Almaty},
		"Шымбулак":     {43.123, 77.093, Almaty},
		"Конаев":       {43.87, 77.07, Region},
		"Каскелен":     {43.20, 76.62, Region},
		"Талгар":       {43.30, 77.24, Region},
		"Чарын":        {43.325, 79.058, Region},
		"Бишкек":       {42.87, 74.59, ""},
		"Каракол":      {42.49, 78.39, ""},
		"Талдыкорган":  {45.02, 78.37, ""},
		"Жаркент":      {44.16, 80.00, ""},
		"Астана":       {51.13, 71.43, ""},
	} {
		if got := AreaOf(c.lat, c.lon); got != c.want {
			t.Errorf("%s: %q, ждали %q", name, got, c.want)
		}
	}
}

func TestOffSeason(t *testing.T) {
	summer := &Place{Season: "summer"}
	winter := &Place{Season: "winter"}
	indoorWinter := &Place{Season: "winter", Indoor: true}
	allYear := &Place{Season: "all_year"}
	for m := time.January; m <= time.December; m++ {
		cold := m >= time.November || m <= time.March
		if got := summer.OffSeason(m); got != cold {
			t.Errorf("летнее место, %v: %v", m, got)
		}
		if got := winter.OffSeason(m); got != (m >= time.May && m <= time.October) {
			t.Errorf("зимнее место, %v: %v", m, got)
		}
		if indoorWinter.OffSeason(m) || allYear.OffSeason(m) {
			t.Errorf("%v: у мест в помещении и круглогодичных сезона нет", m)
		}
	}
}
