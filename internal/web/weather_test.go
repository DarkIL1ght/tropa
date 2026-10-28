package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"tropa/internal/meetups"
	"tropa/internal/places"
	"tropa/internal/weather"
)

// fakeWeather — поддельный Open-Meteo: handler решает, что ответить.
func fakeWeather(t *testing.T, handler http.HandlerFunc) func(*Server) {
	t.Helper()
	api := httptest.NewServer(handler)
	t.Cleanup(api.Close)
	return func(s *Server) {
		c := weather.New(43.25, 76.95)
		c.BaseURL = api.URL
		s.weather = c
	}
}

func reply(status int, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		w.Write([]byte(body))
	}
}

func TestMapWeatherBanner(t *testing.T) {
	const banner = `class="weather"`
	cases := []struct {
		name    string
		handler http.HandlerFunc
		want    string // пусто — плашки быть не должно
	}{
		{"снег", reply(200, `{"current":{"temperature_2m":-8.2,"precipitation":0.4,"weather_code":73,"wind_speed_10m":4}}`), "−8 °C, снег"},
		{"тепло", reply(200, `{"current":{"temperature_2m":18,"precipitation":0,"weather_code":1,"wind_speed_10m":6}}`), ""},
		{"500", reply(500, `{"error":true}`), ""},
		{"мусор", reply(200, `<html>`), ""},
		{"таймаут", func(w http.ResponseWriter, r *http.Request) {
			select { // дольше таймаута клиента в 3 с
			case <-time.After(5 * time.Second):
			case <-r.Context().Done():
			}
		}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ts, _ := newTestEnv(t, fakeWeather(t, tc.handler))
			start := time.Now()
			code, body := newClient(t, ts.URL).get("/")
			if code != http.StatusOK {
				t.Fatalf("главная: %d", code)
			}
			if d := time.Since(start); d > 4*time.Second {
				t.Fatalf("главная ждала погоду %v", d)
			}
			if tc.want == "" {
				if strings.Contains(body, banner) {
					t.Fatal("плашки быть не должно")
				}
				return
			}
			if !strings.Contains(body, banner) || !strings.Contains(body, tc.want) || !strings.Contains(body, `data-cat="indoor"`) {
				t.Fatalf("ждали плашку %q", tc.want)
			}
		})
	}
}

func TestOffSeason(t *testing.T) {
	january := func(s *Server) {
		s.now = func() time.Time { return time.Date(2027, 1, 15, 12, 0, 0, 0, meetups.Almaty) }
	}
	ts, db := newTestEnv(t, january)
	seedPlaces(t, places.New(db), &places.Place{
		Slug: "summer-test", Name: "Летняя поляна", Category: places.Nature, Kind: "viewpoint",
		Lat: 43.2, Lon: 76.95, Area: places.Almaty, Season: "summer",
	})
	c := newClient(t, ts.URL)

	_, body := c.get("/places/summer-test")
	if !strings.Contains(body, "Сейчас не сезон") {
		t.Fatal("на странице летнего места зимой ждали «не сезон»")
	}
	_, body = c.get("/api/places?q=" + "Летняя")
	if !strings.Contains(body, `"off_season":true`) {
		t.Fatalf("в API ждали off_season: %s", body)
	}
}
