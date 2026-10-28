package weather

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// fakeAPI — поддельный Open-Meteo: отдаёт body со статусом status и считает запросы.
func fakeAPI(t *testing.T, status int, body string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var n atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		if r.URL.Query().Get("current") == "" || r.URL.Query().Get("latitude") != "43.2500" {
			t.Errorf("запрос без нужных параметров: %s", r.URL.RawQuery)
		}
		w.WriteHeader(status)
		w.Write([]byte(body))
	}))
	t.Cleanup(ts.Close)
	return ts, &n
}

func newClient(base string, now *time.Time) *Client {
	c := New(43.25, 76.95)
	c.BaseURL = base
	c.Now = func() time.Time { return *now }
	return c
}

const snowy = `{"current":{"time":"2026-01-10T12:00","temperature_2m":-8.4,"precipitation":0.3,"weather_code":73,"wind_speed_10m":5.0}}`

func TestCurrentAndCache(t *testing.T) {
	ts, n := fakeAPI(t, 200, snowy)
	now := time.Date(2026, 1, 10, 12, 0, 0, 0, time.UTC)
	c := newClient(ts.URL, &now)

	w, err := c.Current(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if w.TempC != -8.4 || w.Code != 73 || !w.Cold() || w.Temp() != "−8 °C" || w.Label() != "снег" {
		t.Fatalf("разбор: %+v %q %q", w, w.Temp(), w.Label())
	}

	now = now.Add(29 * time.Minute)
	c.Current(context.Background())
	if n.Load() != 1 {
		t.Fatalf("в пределах 30 минут ждали кэш, запросов: %d", n.Load())
	}
	now = now.Add(2 * time.Minute)
	c.Current(context.Background())
	if n.Load() != 2 {
		t.Fatalf("после 30 минут ждали новый запрос, запросов: %d", n.Load())
	}
}

func TestFailures(t *testing.T) {
	cases := map[string]struct {
		status int
		body   string
	}{
		"500":         {500, `{"error":true}`},
		"битый JSON":  {200, `{"current":`},
		"нет current": {200, `{"latitude":43.25}`},
		"нет темп.":   {200, `{"current":{"weather_code":3}}`},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ts, n := fakeAPI(t, tc.status, tc.body)
			now := time.Now()
			c := newClient(ts.URL, &now)
			if _, err := c.Current(context.Background()); err == nil {
				t.Fatal("ждали ошибку")
			}
			// повтор в течение минуты не ходит в упавший сервис
			c.Current(context.Background())
			if n.Load() != 1 {
				t.Fatalf("запросов: %d", n.Load())
			}
			now = now.Add(61 * time.Second)
			c.Current(context.Background())
			if n.Load() != 2 {
				t.Fatalf("через минуту ждали повтор, запросов: %d", n.Load())
			}
		})
	}
}

func TestTimeout(t *testing.T) {
	block := make(chan struct{})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-block:
		case <-r.Context().Done():
		}
	}))
	defer ts.Close()
	defer close(block)
	now := time.Now()
	c := newClient(ts.URL, &now)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := c.Current(ctx); err == nil {
		t.Fatal("ждали ошибку по таймауту")
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("ждали слишком долго: %v", d)
	}
}

func TestCold(t *testing.T) {
	cases := []struct {
		w    Weather
		cold bool
	}{
		{Weather{TempC: 12, Code: 1, WindKmh: 10}, false},
		{Weather{TempC: 0.2, Code: 3}, false},
		{Weather{TempC: -0.4, Code: 0}, true}, // мороз
		{Weather{TempC: 15, Code: 61}, true},  // дождь по коду
		{Weather{TempC: 15, Code: 3, PrecipMM: 0.1}, true},
		{Weather{TempC: 15, Code: 0, WindKmh: 40}, true}, // ветер
		{Weather{TempC: 15, Code: 45}, false},            // туман — не повод
	}
	for _, tc := range cases {
		if got := tc.w.Cold(); got != tc.cold {
			t.Errorf("%+v: Cold=%v, ждали %v", tc.w, got, tc.cold)
		}
	}
	for temp, want := range map[float64]string{3.4: "+3 °C", -0.4: "0 °C", 0: "0 °C", -12.6: "−13 °C"} {
		if got := (Weather{TempC: temp}).Temp(); got != want {
			t.Errorf("Temp(%v) = %q, ждали %q", temp, got, want)
		}
	}
}

func TestSummary(t *testing.T) {
	cases := map[string]Weather{
		"−8 °C, снег":                {TempC: -8.4, Code: 73},
		"+5 °C, ясно, сильный ветер": {TempC: 5, Code: 0, WindKmh: 50},
		"+3 °C": {TempC: 3, Code: 99_000}, // неизвестный код — без подписи
	}
	for want, w := range cases {
		if got := w.Summary(); got != want {
			t.Errorf("%+v: %q, ждали %q", w, got, want)
		}
	}
}
