package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"

	"tropa/internal/places"
)

func TestParsePlaceQuery(t *testing.T) {
	ok := []string{
		"",
		"bbox=43.1,76.8,43.3,77.0",
		"bbox=43.1, 76.8, 43.3, 77.0&cat=nature,indoor&indoor=1&free=1&q=медеу",
	}
	for _, raw := range ok {
		v, _ := url.ParseQuery(raw)
		if _, err := parsePlaceQuery(v); err != nil {
			t.Errorf("%q: %v", raw, err)
		}
	}
	v, _ := url.ParseQuery("bbox=43.1,76.8,43.3,77.0&cat=nature,indoor&indoor=1")
	q, _ := parsePlaceQuery(v)
	if q.BBox == nil || q.BBox.East != 77.0 || len(q.Categories) != 2 || !q.IndoorOnly || q.FreeOnly {
		t.Fatalf("разбор: %+v", q)
	}
	bad := []string{
		"bbox=43.1,76.8,43.3",
		"bbox=a,b,c,d",
		"bbox=43.3,76.8,43.1,77.0", // юг севернее севера
		"bbox=43.1,76.8,95,77.0",
		"cat=food",
		"q=" + strings.Repeat("я", 101),
	}
	for _, raw := range bad {
		v, _ := url.ParseQuery(raw)
		if _, err := parsePlaceQuery(v); err == nil {
			t.Errorf("%q: ждали ошибку", raw)
		}
	}
}

func seedPlaces(t *testing.T, s *places.Service, extra ...*places.Place) {
	t.Helper()
	list, err := places.LoadSeed(os.DirFS("../../data/places"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Seed(context.Background(), append(list, extra...)); err != nil {
		t.Fatal(err)
	}
}

func TestPlacesAPIAndPage(t *testing.T) {
	ts, db := newTestEnv(t)
	seedPlaces(t, places.New(db), &places.Place{
		Slug: "xss-test", Name: `<img src=x onerror=alert(1)>`, Category: places.Indoor, Kind: "cafe",
		Lat: 43.25, Lon: 76.95, Area: places.Almaty, Indoor: true,
	})
	c := newClient(t, ts.URL)

	getFC := func(query string) (int, featureCollection) {
		t.Helper()
		resp, err := c.http.Get(ts.URL + "/api/places?" + query)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var fc featureCollection
		if resp.StatusCode == 200 {
			if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
				t.Fatalf("Content-Type %s", ct)
			}
			if err := json.NewDecoder(resp.Body).Decode(&fc); err != nil {
				t.Fatal(err)
			}
		}
		return resp.StatusCode, fc
	}

	code, all := getFC("")
	if code != 200 || all.Type != "FeatureCollection" || len(all.Features) < 20 {
		t.Fatalf("все места: %d, %d шт.", code, len(all.Features))
	}
	for _, f := range all.Features {
		lon, lat := f.Geometry.Coordinates[0], f.Geometry.Coordinates[1]
		if f.Type != "Feature" || f.Geometry.Type != "Point" || !places.RegionBBox.Contains(lat, lon) {
			t.Fatalf("кривая точка %+v", f)
		}
		if f.Properties.URL != "/places/"+f.Properties.Slug || f.Properties.Kind == "" {
			t.Fatalf("свойства %+v", f.Properties)
		}
	}

	_, city := getFC("bbox=43.0,76.7,43.45,77.2")
	if len(city.Features) == 0 || len(city.Features) >= len(all.Features) {
		t.Fatalf("город: %d из %d", len(city.Features), len(all.Features))
	}
	_, culture := getFC("cat=culture&indoor=1")
	for _, f := range culture.Features {
		if f.Properties.Category != "culture" || !f.Properties.Indoor {
			t.Fatalf("фильтр культура+в тепле: %+v", f.Properties)
		}
	}
	if len(culture.Features) == 0 {
		t.Fatal("культура в тепле — пусто")
	}
	if _, found := getFC("q=" + url.QueryEscape("Медеу")); len(found.Features) != 1 || found.Features[0].Properties.Slug != "medeu" {
		t.Fatalf("поиск Медеу: %+v", found.Features)
	}
	if code, _ := getFC("bbox=1,2,3"); code != http.StatusBadRequest {
		t.Fatalf("кривой bbox: %d", code)
	}

	// страница места
	code, body := c.get("/places/medeu")
	if code != 200 || !strings.Contains(body, "Медеу") || !strings.Contains(body, "https://2gis.kz/almaty/search/") ||
		!strings.Contains(body, "https://www.openstreetmap.org/way/171504564") || !strings.Contains(body, `data-lat="43.15718"`) {
		t.Fatalf("страница Медеу: %d", code)
	}
	if code, _ := c.get("/places/no-such-place"); code != http.StatusNotFound {
		t.Fatalf("нет места: %d", code)
	}
	_, body = c.get("/places/xss-test")
	if strings.Contains(body, "<img src=x") || !strings.Contains(body, "&lt;img src=x") {
		t.Fatal("название места не экранировано")
	}

	// главная — карта, CSP пускает только плитки OSM
	resp, err := c.http.Get(ts.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	csp := resp.Header.Get("Content-Security-Policy")
	if resp.StatusCode != 200 || !strings.Contains(csp, "img-src 'self' data: https://tile.openstreetmap.org;") ||
		!strings.Contains(csp, "script-src 'self';") {
		t.Fatalf("главная: %d, CSP %q", resp.StatusCode, csp)
	}
	if code, body := c.get("/"); code != 200 || !strings.Contains(body, `id="map"`) || !strings.Contains(body, "/static/vendor/leaflet/leaflet.js") {
		t.Fatal("на главной нет карты")
	}
	for _, path := range []string{"/static/map.js", "/static/vendor/leaflet/leaflet.js", "/static/vendor/markercluster/leaflet.markercluster.js"} {
		if code, _ := c.get(path); code != 200 {
			t.Fatalf("%s: %d", path, code)
		}
	}
}
