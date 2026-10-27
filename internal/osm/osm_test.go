package osm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"tropa/internal/places"
)

func TestSlugify(t *testing.T) {
	for in, want := range map[string]string{
		"Парк имени 28 гвардейцев-панфиловцев": "park-imeni-28-gvardeytsev-panfilovtsev",
		"Көлсай көлдері":                       "kolsay-kolderi",
		`ТРЦ "MART Village"`:                   "trts-mart-village",
		"Lumièra":                              "lumira",
		"  Бұлақ 💦 ":                           "bulak",
		"Щучье, ёлка":                          "schuche-elka",
		"!!!":                                  "",
	} {
		if got := Slugify(in); got != want {
			t.Errorf("Slugify(%q) = %q, ждали %q", in, got, want)
		}
	}
	if long := Slugify(strings.Repeat("очень длинное название ", 10)); len(long) > 70 || strings.HasSuffix(long, "-") {
		t.Errorf("длинный slug: %q", long)
	}
}

func node(id int64, lat, lon float64, tags ...string) Element {
	e := Element{Type: "node", ID: id, Lat: lat, Lon: lon, Tags: map[string]string{}}
	for i := 0; i+1 < len(tags); i += 2 {
		e.Tags[tags[i]] = tags[i+1]
	}
	return e
}

// way с рамкой заданного размера (≈ size метров по широте) вокруг точки.
func way(id int64, lat, lon, size float64, tags ...string) Element {
	e := node(id, 0, 0, tags...)
	e.Type = "way"
	d := size / 111_000 / 2
	e.Bounds = &Bounds{MinLat: lat - d, MinLon: lon - d, MaxLat: lat + d, MaxLon: lon + d}
	return e
}

const cityLat, cityLon = 43.238, 76.925 // центр Алматы

func TestClassify(t *testing.T) {
	cases := []struct {
		name string
		e    Element
		kind places.Kind // пусто — объект отбрасывается
	}{
		{"антикафе по названию", node(1, cityLat, cityLon, "amenity", "cafe", "name", "Антикафе Ёж"), "anticafe"},
		{"обычное кафе — нет", node(2, cityLat, cityLon, "amenity", "cafe", "name", "Кофейня"), ""},
		{"кино", node(3, cityLat, cityLon, "amenity", "cinema", "name", "Арман"), "cinema"},
		{"квест-комната", node(4, cityLat, cityLon, "leisure", "escape_game", "name", "Фобия"), "quest"},
		{"боулинг", node(5, cityLat, cityLon, "leisure", "bowling_alley", "name", "Bastau"), "bowling"},
		{"музей", node(6, cityLat, cityLon, "tourism", "museum", "name", "Музей"), "museum"},
		{"большой парк", way(7, cityLat, cityLon, 800, "leisure", "park", "name", "Южный парк"), "park"},
		{"сквер у дома — нет", way(8, cityLat, cityLon, 120, "leisure", "park", "name", "Сквер"), ""},
		{"пик в горах", node(9, 43.10, 77.0, "natural", "peak", "name", "Кумбель", "ele", "3221"), "peak"},
		{"пик без высоты — нет", node(10, 43.10, 77.0, "natural", "peak", "name", "Безымянка"), ""},
		{"пик далеко от города — нет", node(11, 44.5, 78.0, "natural", "peak", "name", "Дальний", "ele", "900"), ""},
		{"без названия — нет", node(12, cityLat, cityLon, "amenity", "cinema"), ""},
		{"название из цифр — нет", node(13, cityLat, cityLon, "amenity", "cinema", "name", "13"), ""},
		{"уличный каток — зима", way(14, cityLat, cityLon, 60, "leisure", "ice_rink", "name", "Каток"), "rink"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, ok := Classify(&c.e)
			if c.kind == "" {
				if ok {
					t.Fatalf("ждали отказ, получили %+v", r)
				}
				return
			}
			if !ok || r.kind != c.kind {
				t.Fatalf("ждали %s, получили %+v ok=%v", c.kind, r, ok)
			}
		})
	}
	r, _ := Classify(&cases[13].e)
	if r.indoor || r.season != "winter" {
		t.Errorf("уличный каток: %+v", r)
	}
	free := node(15, cityLat, cityLon, "tourism", "museum", "name", "Бесплатный музей", "fee", "no")
	if r, _ := Classify(&free); !r.free {
		t.Error("fee=no должен давать free")
	}
}

func TestCandidates(t *testing.T) {
	seed := []*places.Place{{
		Slug: "kok-tobe", Name: "Кок-Тобе", Category: places.Nature, Kind: "viewpoint",
		Lat: 43.2331, Lon: 76.9755, Area: places.Almaty, OSMRef: "node/100",
	}}
	elems := []Element{
		node(100, 43.2331, 76.9755, "tourism", "viewpoint", "name", "Кок-Тобе"),  // уже есть по osm_ref
		node(101, 43.2335, 76.9760, "tourism", "viewpoint", "name", "Кок-Тобе"),  // то же название рядом
		node(102, cityLat, cityLon, "amenity", "cinema", "name", "Арман"),        // город
		node(103, 43.87, 77.07, "amenity", "cinema", "name", "Арман"),            // Конаев: область, тот же slug
		node(104, 42.87, 74.59, "amenity", "cinema", "name", "Бишкек кино"),      // Кыргызстан — нет
		node(105, 45.02, 78.37, "amenity", "cinema", "name", "Талдыкорган кино"), // Жетысуская область — нет
		node(106, cityLat, cityLon, "amenity", "cinema", "name", "Lumièra", "website", "lumiera.kz"),
	}
	got := Candidates(elems, NewExisting(seed))
	bySlug := map[string]*places.Place{}
	for _, p := range got {
		bySlug[p.Slug] = p
		if p.Status != places.Draft || p.Source != "osm" || len(p.Validate()) > 0 {
			t.Errorf("кандидат %s: %+v %v", p.Slug, p, p.Validate())
		}
	}
	if len(got) != 3 {
		t.Fatalf("ждали 3 кандидата, получили %d: %v", len(got), bySlug)
	}
	if p := bySlug["arman"]; p == nil || p.Area != places.Almaty || p.OSMRef != "node/102" {
		t.Errorf("Арман в городе: %+v", p)
	}
	if p := bySlug["arman-2"]; p == nil || p.Area != places.Region {
		t.Errorf("Арман в Конаеве: %+v", p)
	}
	if p := bySlug["lumira"]; p == nil || p.Website != "https://lumiera.kz" {
		t.Errorf("сайт без схемы: %+v", p)
	}
}

func TestQueryIncludesAllSelectors(t *testing.T) {
	q := Query(places.RegionBBox)
	if !strings.HasPrefix(q, "[out:json]") || !strings.HasSuffix(q, "out tags bb;\n") {
		t.Fatalf("запрос:\n%s", q)
	}
	if n := strings.Count(q, "(42.1000,74.0000,46.8000,80.6000);"); n != len(selectors) {
		t.Fatalf("рамка у %d селекторов из %d", n, len(selectors))
	}
}

func TestFetchRetriesAndFallback(t *testing.T) {
	body, _ := json.Marshal(response{Elements: []Element{node(1, cityLat, cityLon, "amenity", "cinema", "name", "Арман")}})

	var calls atomic.Int32
	flaky := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") != UserAgent || !strings.Contains(r.FormValue("data"), "out tags bb") {
			t.Errorf("запрос без User-Agent или data")
		}
		switch calls.Add(1) {
		case 1:
			w.WriteHeader(http.StatusTooManyRequests)
		case 2:
			w.Write([]byte(`{"elements":[],"remark":"runtime error: Query timed out"}`))
		default:
			w.Write(body)
		}
	}))
	defer flaky.Close()

	c := &Client{Endpoints: []string{flaky.URL}, Retries: 3, Backoff: time.Millisecond}
	els, err := c.Fetch(context.Background(), "out tags bb;")
	if err != nil || len(els) != 1 || calls.Load() != 3 {
		t.Fatalf("повторы: els=%d calls=%d err=%v", len(els), calls.Load(), err)
	}

	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusBadGateway) }))
	defer down.Close()
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(body) }))
	defer ok.Close()
	c = &Client{Endpoints: []string{down.URL, ok.URL}, Retries: 2, Backoff: time.Millisecond}
	if els, err := c.Fetch(context.Background(), "out tags bb;"); err != nil || len(els) != 1 {
		t.Fatalf("запасной сервер: %v", err)
	}

	var badCalls atomic.Int32
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		badCalls.Add(1)
		http.Error(w, "parse error: line 1", http.StatusBadRequest)
	}))
	defer bad.Close()
	c = &Client{Endpoints: []string{bad.URL}, Retries: 3, Backoff: time.Millisecond}
	if _, err := c.Fetch(context.Background(), "мусор"); err == nil || !strings.Contains(err.Error(), "parse error") || badCalls.Load() != 1 {
		t.Fatalf("ошибка в запросе не должна повторяться: calls=%d err=%v", badCalls.Load(), err)
	}
}

func TestQuotes(t *testing.T) {
	for in, want := range map[string]string{
		`ТРЦ "Forum Almaty"`: "ТРЦ «Forum Almaty»",
		`"Grand Park" ТРЦ`:   "«Grand Park» ТРЦ",
		`Театр "А" и "Б"`:    "Театр «А» и «Б»",
		`Кривая "кавычка`:    `Кривая "кавычка`,
		"Без кавычек":        "Без кавычек",
	} {
		if got := Quotes(in); got != want {
			t.Errorf("Quotes(%q) = %q, ждали %q", in, got, want)
		}
	}
}
