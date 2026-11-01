package web

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"tropa/internal/places"
)

// userID — id вошедшего пользователя по ссылке на его страницу в шапке профиля.
func userID(t *testing.T, c *client) string {
	t.Helper()
	_, body := c.get("/friends")
	const mark = "/u/"
	i := strings.Index(body, `readonly value="`)
	if i < 0 {
		t.Fatal("на /friends нет ссылки на свою страницу")
	}
	link := body[i+len(`readonly value="`):]
	link = link[:strings.Index(link, `"`)]
	return link[strings.LastIndex(link, mark)+len(mark):]
}

func TestDirectStream(t *testing.T) {
	ts := newTestServer(t)
	a := signup(t, ts.URL, "a@tropa.kz")
	b := signup(t, ts.URL, "b@tropa.kz")
	aID, bID := userID(t, a), userID(t, b)
	a.post("/friends", "/u/"+bID+"/friend", url.Values{"action": {"add"}})
	b.post("/friends", "/u/"+aID+"/friend", url.Values{"action": {"accept"}})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", ts.URL+"/messages/"+aID+"/stream?after=0", nil)
	resp, err := b.http.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("поток: %d", resp.StatusCode)
	}
	lines := bufio.NewScanner(resp.Body)
	waitFor := func(prefix string) string {
		t.Helper()
		for lines.Scan() {
			if l := lines.Text(); strings.HasPrefix(l, prefix) {
				return strings.TrimPrefix(l, prefix)
			}
		}
		t.Fatalf("поток закончился, не дождались %q", prefix)
		return ""
	}
	waitFor(": ok")
	a.post("/messages/"+bID, "/messages/"+bID, url.Values{"body": {"<i>едем</i>"}})
	var ev chatEvent
	if err := json.Unmarshal([]byte(waitFor("data: ")), &ev); err != nil || ev.Body != "<i>едем</i>" || ev.UserID == 0 {
		t.Fatalf("событие: %+v %v", ev, err)
	}
	// пришло в открытую переписку — уже прочитано
	if _, body := b.get("/messages"); strings.Contains(body, "непрочитанных:") {
		t.Fatal("сообщение в открытом потоке осталось непрочитанным")
	}
	// постороннему поток чужой переписки закрыт
	c := signup(t, ts.URL, "c@tropa.kz")
	if code, _ := c.get("/messages/" + aID + "/stream"); code != http.StatusNotFound {
		t.Fatalf("чужой поток: %d", code)
	}
}

func TestUnverifiedOnlyLooks(t *testing.T) {
	ts, db := newTestEnv(t)
	seedPlaces(t, places.New(db), &places.Place{
		Slug: "test-park", Name: "Тестовый парк", Category: places.Nature, Kind: "park",
		Lat: 43.22, Lon: 76.93, Area: places.Almaty,
	})
	c := signupUnverified(t, ts.URL, "new@tropa.kz")

	// смотреть можно, с плашкой «подтвердите почту»
	if code, body := c.get("/meetups"); code != 200 || !strings.Contains(body, "Подтвердите почту") {
		t.Fatalf("список встреч: %d", code)
	}
	// создать встречу — нельзя: страница кода с возвратом назад
	resp, err := c.http.Get(ts.URL + "/places/test-park/meetups/new")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/verify?next=%2Fplaces%2Ftest-park%2Fmeetups%2Fnew" {
		t.Fatalf("новая встреча без подтверждения: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	if resp := c.post("/places/test-park", "/places/test-park/meetups", meetupForm("Сходка", "", "public")); resp.StatusCode != http.StatusSeeOther ||
		!strings.HasPrefix(resp.Header.Get("Location"), "/verify") {
		t.Fatalf("создание встречи без подтверждения: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	var n int
	db.QueryRow(t.Context(), `SELECT count(*) FROM meetups`).Scan(&n)
	if n != 0 {
		t.Fatalf("встреча создана без подтверждения почты: %d", n)
	}

	// неверный код — ошибка; верный — всё открыто и возвращает, куда шли
	if resp := c.post("/verify", "/verify", url.Values{"code": {"000000"}, "next": {"/places/test-park/meetups/new"}}); resp.StatusCode != http.StatusUnprocessableEntity &&
		testMail.code("new@tropa.kz") != "000000" {
		t.Fatalf("неверный код: %d", resp.StatusCode)
	}
	resp = c.post("/verify", "/verify", url.Values{"code": {testMail.code("new@tropa.kz")}, "next": {"/places/test-park/meetups/new"}})
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/places/test-park/meetups/new" {
		t.Fatalf("подтверждение: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	if code, body := c.get("/meetups"); code != 200 || strings.Contains(body, "Подтвердите почту") {
		t.Fatalf("плашка после подтверждения: %d", code)
	}
}

// Пока письма доходят не до всех, подтверждение выключено: новичок сразу
// пользуется сайтом, плашки нет, в поиске он виден.
func TestVerificationOff(t *testing.T) {
	ts, db := newTestEnv(t, func(s *Server) {
		s.cfg.RequireVerification = false
		s.friends.OnlyVerified = false
	})
	seedPlaces(t, places.New(db), &places.Place{
		Slug: "test-park", Name: "Тестовый парк", Category: places.Nature, Kind: "park",
		Lat: 43.22, Lon: 76.93, Area: places.Almaty,
	})
	c := newClient(t, ts.URL)
	resp := c.post("/register", "/register", url.Values{
		"email": {"free@tropa.kz"}, "name": {"Свободный"}, "password": {"password123"}, "password2": {"password123"},
	})
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/" {
		t.Fatalf("регистрация: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	if _, body := c.get("/meetups"); strings.Contains(body, "Подтвердите почту") {
		t.Fatal("плашка при выключенном подтверждении")
	}
	if resp := c.post("/places/test-park", "/places/test-park/meetups", meetupForm("Сходка", "", "public")); resp.StatusCode != http.StatusSeeOther ||
		!strings.HasPrefix(resp.Header.Get("Location"), "/m/") {
		t.Fatalf("встреча без подтверждения почты: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	other := signupUnverified(t, ts.URL, "other@tropa.kz")
	if _, body := other.get("/friends?q=" + url.QueryEscape("Свобод")); !strings.Contains(body, "Свободный") {
		t.Fatal("неподтверждённый не находится в поиске")
	}
}

func TestResendCode(t *testing.T) {
	ts := newTestServer(t)
	c := signupUnverified(t, ts.URL, "resend@tropa.kz")
	first := testMail.code("resend@tropa.kz")
	// код только что ушёл при регистрации — новый не раньше чем через минуту
	resp := c.post("/verify", "/verify/resend", url.Values{})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("повторная отправка: %d", resp.StatusCode)
	}
	if _, body := c.get("/verify"); !strings.Contains(body, "через минуту") || testMail.code("resend@tropa.kz") != first {
		t.Fatal("второе письмо ушло сразу")
	}
}

func TestFriendSearchAndMessages(t *testing.T) {
	ts := newTestServer(t)
	a := signup(t, ts.URL, "aigerim@tropa.kz")
	b := signup(t, ts.URL, "bekzat@tropa.kz")
	stranger := signup(t, ts.URL, "stranger@tropa.kz")
	signupUnverified(t, ts.URL, "ghost@tropa.kz")
	bID := userID(t, b)
	aID := userID(t, a)

	// поиск по email целиком находит Бекзата, но email на странице не показывается
	code, body := a.get("/friends?q=" + url.QueryEscape("BEKZAT@tropa.kz"))
	if code != 200 || !strings.Contains(body, "Пользователь bekzat@tropa.kz") {
		t.Fatalf("поиск по email: %d", code)
	}
	// по части имени находятся только подтвердившие почту
	if _, body := a.get("/friends?q=tropa.kz"); !strings.Contains(body, "Пользователь bekzat") || strings.Contains(body, "Пользователь ghost") {
		t.Fatal("поиск по имени показывает не тех")
	}

	// не друзья — переписки нет
	if code, _ := a.get("/messages/" + bID); code != http.StatusNotFound {
		t.Fatalf("переписка с не-другом: %d", code)
	}
	if resp := a.post("/friends", "/messages/"+bID, url.Values{"body": {"привет"}}); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("сообщение не-другу: %d", resp.StatusCode)
	}

	// заявка из поиска возвращает к тем же результатам
	resp := a.post("/friends", "/u/"+bID+"/friend", url.Values{"action": {"add"}, "next": {"/friends?q=bekzat"}})
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/friends?q=bekzat" {
		t.Fatalf("заявка из поиска: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	b.post("/friends", "/u/"+aID+"/friend", url.Values{"action": {"accept"}})

	if resp := a.post("/messages/"+bID, "/messages/"+bID, url.Values{"body": {"Салам! Идём в горы?"}}); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("отправка: %d", resp.StatusCode)
	}
	// у Бекзата счётчик в шапке и сообщение в переписке
	if _, body := b.get("/messages"); !strings.Contains(body, `aria-label="непрочитанных: 1"`) || !strings.Contains(body, "Идём в горы?") {
		t.Fatal("нет непрочитанного у получателя")
	}
	if code, body := b.get("/messages/" + aID); code != 200 || !strings.Contains(body, "Салам! Идём в горы?") {
		t.Fatalf("переписка у получателя: %d", code)
	}
	if _, body := b.get("/messages"); strings.Contains(body, "непрочитанных:") {
		t.Fatal("после открытия переписки счётчик не сбросился")
	}

	// третий не видит чужую переписку
	if code, _ := stranger.get("/messages/" + aID); code != http.StatusNotFound {
		t.Fatalf("чужая переписка: %d", code)
	}
	if code, _ := stranger.get("/messages/" + strconv.Itoa(999999)); code != http.StatusNotFound {
		t.Fatalf("несуществующий: %d", code)
	}
}
