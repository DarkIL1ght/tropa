package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestFormat(t *testing.T) {
	d := func(y, m, day int) time.Time { return time.Date(y, time.Month(m), day, 0, 0, 0, 0, time.UTC) }
	for _, c := range []struct {
		got, want string
	}{
		{dateRange(d(2027, 3, 12), d(2027, 3, 14)), "12–14 марта 2027"},
		{dateRange(d(2027, 3, 30), d(2027, 4, 2)), "30 марта – 2 апреля 2027"},
		{dateRange(d(2027, 12, 30), d(2028, 1, 2)), "30 декабря 2027 – 2 января 2028"},
		{dateRange(d(2027, 5, 5), d(2027, 5, 5)), "5 мая 2027"},
		{money(0), "бесплатно"},
		{money(500), "500 ₸"},
		{money(15000), "15 000 ₸"},
		{money(1250000), "1 250 000 ₸"},
		{plural(1, "день", "дня", "дней"), "день"},
		{plural(3, "день", "дня", "дней"), "дня"},
		{plural(5, "день", "дня", "дней"), "дней"},
		{plural(11, "день", "дня", "дней"), "дней"},
		{plural(21, "день", "дня", "дней"), "день"},
		{plural(112, "день", "дня", "дней"), "дней"},
	} {
		if c.got != c.want {
			t.Errorf("%q, ждали %q", c.got, c.want)
		}
	}
}

// signup регистрирует нового пользователя в своём «браузере».
func signup(t *testing.T, base, email string) *client {
	c := newClient(t, base)
	resp := c.post("/register", "/register", url.Values{
		"email": {email}, "name": {"Пользователь"}, "password": {"password123"}, "password2": {"password123"},
	})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("регистрация %s: %d", email, resp.StatusCode)
	}
	return c
}

func tripForm(title string) url.Values {
	start := time.Now().AddDate(0, 0, 10)
	return url.Values{
		"title": {title}, "region": {"Алматинская область"}, "difficulty": {"medium"},
		"start_date": {start.Format("2006-01-02")}, "end_date": {start.AddDate(0, 0, 2).Format("2006-01-02")},
		"capacity": {"12"}, "fee": {"15000"}, "description": {"Маршрут\nвторая строка"},
	}
}

func TestTripFlow(t *testing.T) {
	ts := newTestServer(t)
	org := signup(t, ts.URL, "org@tropa.kz")
	other := signup(t, ts.URL, "other@tropa.kz")
	guest := newClient(t, ts.URL)

	if resp, _ := guest.http.Get(ts.URL + "/trips/new"); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("гость на /trips/new: %d", resp.StatusCode)
	}

	bad := tripForm("")
	if resp := org.post("/trips/new", "/trips", bad); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("пустое название: %d", resp.StatusCode)
	}

	resp := org.post("/trips/new", "/trips", tripForm(`Кольсай <script>alert(1)</script>`))
	loc := resp.Header.Get("Location")
	if resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(loc, "/trips/") {
		t.Fatalf("создание: %d %s", resp.StatusCode, loc)
	}

	code, body := org.get(loc)
	if code != 200 || !strings.Contains(body, "Черновик") || !strings.Contains(body, "Открыть запись") {
		t.Fatalf("организатор видит черновик: %d", code)
	}
	if strings.Contains(body, "<script>alert") || !strings.Contains(body, "&lt;script&gt;") {
		t.Fatal("название не экранировано")
	}

	// черновик для остальных не существует
	for name, c := range map[string]*client{"гость": guest, "другой": other} {
		if code, _ := c.get(loc); code != http.StatusNotFound {
			t.Fatalf("%s видит черновик: %d", name, code)
		}
	}
	if code, body := guest.get("/trips"); code != 200 || strings.Contains(body, "Кольсай") {
		t.Fatal("черновик попал в каталог")
	}

	if resp := org.post(loc, loc+"/status", url.Values{"from": {"draft"}, "to": {"open"}}); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("открытие записи: %d", resp.StatusCode)
	}
	code, body = guest.get(loc)
	if code != 200 || !strings.Contains(body, "Идёт запись") || strings.Contains(body, "Управление") {
		t.Fatalf("гость на открытом походе: %d", code)
	}
	if _, body := guest.get("/trips?q=кольсай"); !strings.Contains(body, "Кольсай") {
		t.Fatal("нет в каталоге после открытия")
	}
	if _, body := guest.get("/"); !strings.Contains(body, "Кольсай") {
		t.Fatal("нет на главной")
	}

	// чужой пользователь не правит и не меняет статус
	if code, _ := other.get(loc + "/edit"); code != http.StatusForbidden {
		t.Fatalf("чужая форма правки: %d", code)
	}
	if resp := other.post(loc, loc+"/edit", tripForm("Захват")); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("чужая правка: %d", resp.StatusCode)
	}
	if resp := other.post(loc, loc+"/status", url.Values{"from": {"open"}, "to": {"cancelled"}}); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("чужая отмена: %d", resp.StatusCode)
	}

	if resp := org.post(loc+"/edit", loc+"/edit", tripForm("Кольсай и Кайынды")); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("правка: %d", resp.StatusCode)
	}
	if _, body := org.get("/me"); !strings.Contains(body, "Кольсай и Кайынды") {
		t.Fatal("поход не виден в профиле организатора")
	}

	for _, path := range []string{"/trips/abc", "/trips/0", "/trips/999999"} {
		if code, _ := guest.get(path); code != http.StatusNotFound {
			t.Fatalf("%s: %d", path, code)
		}
	}
}
