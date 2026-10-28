package web

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"tropa/internal/places"
)

func TestFriendsFlow(t *testing.T) {
	ts, db := newTestEnv(t)
	seedPlaces(t, places.New(db), &places.Place{
		Slug: "test-park", Name: "Тестовый парк", Category: places.Nature, Kind: "park",
		Lat: 43.22, Lon: 76.93, Area: places.Almaty,
	})
	ctx := context.Background()
	dar := signup(t, ts.URL, "dar@tropa.kz")
	aru := signup(t, ts.URL, "aru@tropa.kz")
	var darID, aruID string
	db.QueryRow(ctx, `SELECT id::text FROM users WHERE email = 'dar@tropa.kz'`).Scan(&darID)
	db.QueryRow(ctx, `SELECT id::text FROM users WHERE email = 'aru@tropa.kz'`).Scan(&aruID)

	// встреча «для друзей» Дархана: Аружан её пока не видит
	loc := dar.post("/places/test-park/meetups/new", "/places/test-park/meetups", meetupForm("Пикник своих", "", "friends")).Header.Get("Location")
	if _, body := aru.get("/friends"); strings.Contains(body, loc) {
		t.Fatal("встреча друга видна до дружбы")
	}

	// заявка со страницы человека → принятие на /friends
	if _, body := dar.get("/u/" + aruID); !strings.Contains(body, "Добавить в друзья") {
		t.Fatal("нет кнопки «Добавить в друзья»")
	}
	if resp := dar.post("/u/"+aruID, "/u/"+aruID+"/friend", url.Values{"action": {"add"}}); resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/u/"+aruID {
		t.Fatalf("заявка: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	if _, body := dar.get("/u/" + aruID); !strings.Contains(body, "Заявка отправлена") {
		t.Fatal("нет статуса заявки")
	}
	if _, body := aru.get("/me"); !strings.Contains(body, "заявок: 1") {
		t.Fatal("в профиле нет счётчика заявок")
	}
	_, body := aru.get("/friends")
	if !strings.Contains(body, "Заявки в друзья") || !strings.Contains(body, "Пользователь dar@tropa.kz") {
		t.Fatal("на /friends нет входящей заявки")
	}
	if resp := aru.post("/friends", "/u/"+darID+"/friend", url.Values{"action": {"accept"}, "next": {"/friends"}}); resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/friends" {
		t.Fatalf("принятие: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}

	// теперь встреча «для друзей» видна у друга, а у участников — отметка «друг»
	if _, body := aru.get("/friends"); !strings.Contains(body, loc) || !strings.Contains(body, "Мои друзья · 1") {
		t.Fatal("после дружбы встреча друга не видна")
	}
	aru.post(loc, loc+"/join", url.Values{})
	if _, body := dar.get(loc); !strings.Contains(body, `class="friend-tag"`) {
		t.Fatal("нет отметки «друг» у участника")
	}
	if _, body := aru.get("/u/" + darID); !strings.Contains(body, "В друзьях") || !strings.Contains(body, "Друзей: <strong>1</strong>") {
		t.Fatal("на странице друга нет статуса")
	}

	// гость не может ничего сделать с дружбой
	if resp, _ := newClient(t, ts.URL).http.Get(ts.URL + "/friends"); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("гость на /friends: %d", resp.StatusCode)
	}
	// удалить из друзей — встреча снова скрыта
	aru.post("/u/"+darID, "/u/"+darID+"/friend", url.Values{"action": {"remove"}})
	if _, body := aru.get("/friends"); strings.Contains(body, "Пикник своих") {
		t.Fatal("после удаления встреча видна")
	}
}
