package web

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"tropa/internal/places"
)

var userLinkRe = regexp.MustCompile(`href="(/u/\d+)">Моя публичная страница`)

func TestSettings(t *testing.T) {
	ts := newTestServer(t)
	c := signup(t, ts.URL, "a@tropa.kz")
	laptop := newClient(t, ts.URL)
	if resp := laptop.post("/login", "/login", url.Values{"email": {"a@tropa.kz"}, "password": {"password123"}}); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("вход со второго устройства: %d", resp.StatusCode)
	}
	if resp, _ := newClient(t, ts.URL).http.Get(ts.URL + "/me/settings"); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("гость в настройках: %d", resp.StatusCode)
	}

	if resp := c.post("/me/settings", "/me/name", url.Values{"name": {"Я"}}); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("короткое имя: %d", resp.StatusCode)
	}
	if resp := c.post("/me/settings", "/me/name", url.Values{"name": {"Дархан"}}); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("смена имени: %d", resp.StatusCode)
	}
	if _, body := c.get("/me"); !strings.Contains(body, "<h1>Дархан</h1>") {
		t.Fatal("имя не изменилось")
	}

	for name, form := range map[string]url.Values{
		"неверный текущий": {"current": {"wrong-pass"}, "password": {"new-password1"}, "password2": {"new-password1"}},
		"не совпадают":     {"current": {"password123"}, "password": {"new-password1"}, "password2": {"new-password2"}},
		"короткий":         {"current": {"password123"}, "password": {"short"}, "password2": {"short"}},
	} {
		if resp := c.post("/me/settings", "/me/password", form); resp.StatusCode != http.StatusUnprocessableEntity {
			t.Fatalf("%s: %d", name, resp.StatusCode)
		}
	}
	if code, _ := laptop.get("/me"); code != 200 {
		t.Fatal("неудачная смена пароля разлогинила другое устройство")
	}
	resp := c.post("/me/settings", "/me/password", url.Values{
		"current": {"password123"}, "password": {"new-password1"}, "password2": {"new-password1"},
	})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("смена пароля: %d", resp.StatusCode)
	}
	if code, body := c.get("/me/settings"); code != 200 || !strings.Contains(body, "Пароль изменён") {
		t.Fatalf("этот браузер должен остаться в аккаунте: %d", code)
	}
	if resp, _ := laptop.http.Get(ts.URL + "/me"); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("другое устройство не разлогинено: %d", resp.StatusCode)
	}
}

func TestPublicProfile(t *testing.T) {
	ts, db := newTestEnv(t)
	seedPlaces(t, places.New(db), &places.Place{
		Slug: "test-rink", Name: "Тестовый каток", Category: places.Active, Kind: "rink",
		Lat: 43.22, Lon: 76.93, Area: places.Almaty, Season: "winter",
	})
	org := signup(t, ts.URL, "org@tropa.kz")
	org.post("/me/settings", "/me/name", url.Values{"name": {"Организатор"}})
	a := signup(t, ts.URL, "a@tropa.kz")
	const form = "/places/test-rink/meetups/new"
	pub := org.post(form, "/places/test-rink/meetups", meetupForm("Открытый каток", "", "public")).Header.Get("Location")
	friends := org.post(form, "/places/test-rink/meetups", meetupForm("Каток для своих", "", "friends")).Header.Get("Location")
	a.post(friends, friends+"/join", url.Values{})

	_, body := org.get("/me")
	m := userLinkRe.FindStringSubmatch(body)
	if m == nil {
		t.Fatal("в профиле нет ссылки на публичную страницу")
	}
	page := m[1]

	guest := newClient(t, ts.URL)
	code, body := guest.get(page)
	if code != 200 || !strings.Contains(body, "<h1>Организатор</h1>") || !strings.Contains(body, pub) {
		t.Fatalf("публичная страница: %d", code)
	}
	if strings.Contains(body, "org@tropa.kz") {
		t.Fatal("на публичной странице email")
	}
	if strings.Contains(body, friends) || strings.Contains(body, "Каток для своих") {
		t.Fatal("на публичной странице встреча для друзей")
	}
	if strings.Contains(body, "Так вашу страницу видят другие") {
		t.Fatal("гостю показана подсказка владельца")
	}
	if _, body := org.get(page); !strings.Contains(body, "Так вашу страницу видят другие") {
		t.Fatal("владельцу нет подсказки")
	}

	// участники встречи — ссылками на профили
	if _, body := a.get(friends); !strings.Contains(body, `<a href="`+page+`">`) {
		t.Fatal("на странице встречи нет ссылки на организатора")
	}

	for _, p := range []string{"/u/abc", "/u/0", "/u/999999"} {
		if code, _ := guest.get(p); code != http.StatusNotFound {
			t.Fatalf("%s: %d", p, code)
		}
	}
}
