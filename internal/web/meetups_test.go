package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"tropa/internal/meetups"
	"tropa/internal/places"
)

func TestPlural(t *testing.T) {
	for _, c := range []struct {
		got, want string
	}{
		{plural(1, "день", "дня", "дней"), "день"},
		{plural(3, "день", "дня", "дней"), "дня"},
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
		"email": {email}, "name": {"Пользователь " + email}, "password": {"password123"}, "password2": {"password123"},
	})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("регистрация %s: %d", email, resp.StatusCode)
	}
	return c
}

func meetupForm(title, capacity, visibility string) url.Values {
	day := time.Now().In(meetups.Almaty).AddDate(0, 0, 7)
	return url.Values{
		"title": {title}, "date": {day.Format("2006-01-02")}, "time": {"18:30"},
		"capacity": {capacity}, "visibility": {visibility}, "description": {"Встречаемся у входа\nвторая строка"},
	}
}

func TestMeetupFlow(t *testing.T) {
	ts, db := newTestEnv(t)
	seedPlaces(t, places.New(db), &places.Place{
		Slug: "test-rink", Name: "Тестовый каток", Category: places.Active, Kind: "rink",
		Lat: 43.22, Lon: 76.93, Area: places.Almaty, Season: "winter",
	})
	org := signup(t, ts.URL, "org@tropa.kz")
	a := signup(t, ts.URL, "a@tropa.kz")
	b := signup(t, ts.URL, "b@tropa.kz")
	guest := newClient(t, ts.URL)
	const form = "/places/test-rink/meetups/new"

	if resp, _ := guest.http.Get(ts.URL + form); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("гость на форме: %d", resp.StatusCode)
	}
	if code, _ := org.get("/places/no-such/meetups/new"); code != http.StatusNotFound {
		t.Fatalf("форма для несуществующего места: %d", code)
	}
	if resp := org.post(form, "/places/test-rink/meetups", meetupForm("", "2", "public")); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("пустое название: %d", resp.StatusCode)
	}

	resp := org.post(form, "/places/test-rink/meetups", meetupForm(`Каток <script>alert(1)</script>`, "2", "public"))
	loc := resp.Header.Get("Location")
	if resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(loc, "/m/") {
		t.Fatalf("создание: %d %s", resp.StatusCode, loc)
	}
	code, body := org.get(loc)
	if code != 200 || !strings.Contains(body, "Управление") || !strings.Contains(body, "Вы идёте") {
		t.Fatalf("организатор на своей встрече: %d", code)
	}
	if strings.Contains(body, "<script>alert") || !strings.Contains(body, "&lt;script&gt;") {
		t.Fatal("название не экранировано")
	}

	// встреча видна в списке, на странице места и на карте
	if _, body := guest.get("/meetups?q=каток"); !strings.Contains(body, loc) {
		t.Fatal("нет в списке встреч")
	}
	if _, body := guest.get("/places/test-rink"); !strings.Contains(body, loc) {
		t.Fatal("нет на странице места")
	}
	if _, body := guest.get("/api/places?q=" + url.QueryEscape("Тестовый каток")); !strings.Contains(body, `"meetups":1`) {
		t.Fatalf("нет счётчика на карте: %s", body)
	}

	// гость видит кнопку входа, а не записи
	if _, body := guest.get(loc); !strings.Contains(body, "Войдите, чтобы записаться") || strings.Contains(body, "Управление") {
		t.Fatal("гость на встрече")
	}

	// 2 места: организатор + a; b — в очередь
	if resp := a.post(loc, loc+"/join", url.Values{}); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("запись a: %d", resp.StatusCode)
	}
	if _, body := b.get(loc); !strings.Contains(body, "Встать в очередь") {
		t.Fatal("мест нет — ждали кнопку очереди")
	}
	b.post(loc, loc+"/join", url.Values{})
	if _, body := b.get(loc); !strings.Contains(body, "Вы в очереди: 1-е место") {
		t.Fatal("b не в очереди")
	}

	// a уходит — b предложено место, b принимает
	a.post(loc, loc+"/leave", url.Values{})
	if _, body := b.get("/me"); !strings.Contains(body, "Для вас освободилось место") {
		t.Fatal("в профиле нет предложения")
	}
	b.post(loc, loc+"/accept", url.Values{})
	if _, body := b.get(loc); !strings.Contains(body, "Вы идёте") {
		t.Fatal("b не идёт после подтверждения")
	}

	// чужой не управляет
	if code, _ := a.get(loc + "/edit"); code != http.StatusForbidden {
		t.Fatalf("чужая форма правки: %d", code)
	}
	if resp := a.post(loc, loc+"/status", url.Values{"from": {"open"}, "to": {"cancelled"}}); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("чужая отмена: %d", resp.StatusCode)
	}
	if resp := org.post(loc+"/edit", loc+"/edit", meetupForm("Каток вечером", "1", "public")); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("1 место: %d", resp.StatusCode)
	}
	if resp := org.post(loc+"/edit", loc+"/edit", meetupForm("Каток вечером", "", "public")); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("правка: %d", resp.StatusCode)
	}
	if _, body := org.get("/me"); !strings.Contains(body, "Каток вечером") {
		t.Fatal("встреча не видна в профиле организатора")
	}
	if resp := org.post(loc, loc+"/status", url.Values{"from": {"open"}, "to": {"cancelled"}}); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("отмена: %d", resp.StatusCode)
	}
	if _, body := guest.get("/meetups"); strings.Contains(body, loc) {
		t.Fatal("отменённая встреча в списке")
	}

	for _, path := range []string{"/m/abc", "/m/" + strings.Repeat("A", 22), loc + "x"} {
		if code, _ := guest.get(path); code != http.StatusNotFound {
			t.Fatalf("%s: %d", path, code)
		}
	}
}

func TestFriendsMeetupHidden(t *testing.T) {
	ts, db := newTestEnv(t)
	seedPlaces(t, places.New(db), &places.Place{
		Slug: "test-park", Name: "Тестовый парк", Category: places.Nature, Kind: "park",
		Lat: 43.23, Lon: 76.94, Area: places.Almaty,
	})
	org := signup(t, ts.URL, "org@tropa.kz")
	guest := newClient(t, ts.URL)
	resp := org.post("/places/test-park/meetups/new", "/places/test-park/meetups", meetupForm("Секретный пикник", "", "friends"))
	loc := resp.Header.Get("Location")
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("создание: %d", resp.StatusCode)
	}
	for _, path := range []string{"/meetups", "/meetups?q=пикник", "/places/test-park", "/api/places?q=" + url.QueryEscape("Тестовый парк")} {
		if _, body := guest.get(path); strings.Contains(body, "Секретный") || strings.Contains(body, loc) || strings.Contains(body, `"meetups":`) {
			t.Fatalf("встреча для друзей видна на %s", path)
		}
	}
	// по ссылке — видна, и ссылка не уходит в Referer
	resp, err := guest.http.Get(ts.URL + loc)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 || resp.Header.Get("Referrer-Policy") != "same-origin" {
		t.Fatalf("по ссылке: %d %q", resp.StatusCode, resp.Header.Get("Referrer-Policy"))
	}
}
