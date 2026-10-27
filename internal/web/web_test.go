package web

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"tropa/internal/auth"
	"tropa/internal/places"
	"tropa/internal/store/storetest"
	"tropa/internal/trips"
)

// client — браузер с cookie, который умеет доставать CSRF-токен из страницы.
type client struct {
	t    *testing.T
	base string
	http *http.Client
}

func newClient(t *testing.T, base string) *client {
	jar, _ := cookiejar.New(nil)
	return &client{t: t, base: base, http: &http.Client{
		Jar:           jar,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

var csrfRe = regexp.MustCompile(`name="csrf_token" value="([^"]+)"`)

func (c *client) get(path string) (int, string) {
	c.t.Helper()
	resp, err := c.http.Get(c.base + path)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// post отправляет форму с CSRF-токеном, взятым со страницы from.
func (c *client) post(from, path string, form url.Values) *http.Response {
	c.t.Helper()
	_, body := c.get(from)
	m := csrfRe.FindStringSubmatch(body)
	if m == nil {
		c.t.Fatalf("на %s нет csrf_token", from)
	}
	form.Set("csrf_token", m[1])
	resp, err := c.http.PostForm(c.base+path, form)
	if err != nil {
		c.t.Fatal(err)
	}
	resp.Body.Close()
	return resp
}

func newTestServer(t *testing.T) *httptest.Server {
	ts, _ := newTestEnv(t)
	return ts
}

// newTestEnv — сервер и его база (для тестов, которым нужно засеять данные).
func newTestEnv(t *testing.T) (*httptest.Server, *pgxpool.Pool) {
	db := storetest.New(t)
	srv, err := New(Config{}, Services{Auth: auth.New(db), Trips: trips.New(db), Places: places.New(db)},
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts, db
}

func TestAccountFlow(t *testing.T) {
	ts := newTestServer(t)
	c := newClient(t, ts.URL)

	// /me для гостя — редирект на вход с возвратом
	resp, _ := c.http.Get(ts.URL + "/me")
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/login?next=%2Fme" {
		t.Fatalf("гость на /me: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}

	resp = c.post("/register", "/register", url.Values{
		"email": {"a@b.kz"}, "name": {"Дархан"}, "password": {"password123"}, "password2": {"password123"},
	})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("регистрация: %d", resp.StatusCode)
	}
	if code, body := c.get("/"); code != 200 || !strings.Contains(body, "Добро пожаловать") {
		t.Fatalf("после регистрации нет приветствия: %d", code)
	}
	if code, body := c.get("/me"); code != 200 || !strings.Contains(body, "a@b.kz") {
		t.Fatalf("профиль: %d", code)
	}

	if resp := c.post("/me", "/logout", url.Values{}); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("выход: %d", resp.StatusCode)
	}
	if resp, _ := c.http.Get(ts.URL + "/me"); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("после выхода /me доступен: %d", resp.StatusCode)
	}

	// вход с next возвращает на исходную страницу
	resp = c.post("/login", "/login", url.Values{"email": {"A@B.kz"}, "password": {"password123"}, "next": {"/me"}})
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/me" {
		t.Fatalf("вход: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	// неверный пароль — с другого браузера, гостем
	resp = newClient(t, ts.URL).post("/login", "/login", url.Values{"email": {"a@b.kz"}, "password": {"wrong-pass"}})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("неверный пароль: %d", resp.StatusCode)
	}
}

func TestRegisterValidation(t *testing.T) {
	ts := newTestServer(t)
	c := newClient(t, ts.URL)
	resp := c.post("/register", "/register", url.Values{
		"email": {"a@b.kz"}, "name": {"Дархан"}, "password": {"password123"}, "password2": {"другой-пароль"},
	})
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("разные пароли: %d", resp.StatusCode)
	}
}

func TestCSRFRejected(t *testing.T) {
	ts := newTestServer(t)
	c := newClient(t, ts.URL)
	c.get("/login") // есть cookie, но в форме чужой токен
	resp, err := c.http.PostForm(ts.URL+"/login", url.Values{
		"email": {"a@b.kz"}, "password": {"x"}, "csrf_token": {"forged-token-forged-token-forged-token"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("подделанный CSRF: %d, ждали 403", resp.StatusCode)
	}
}
