package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestLimiter(t *testing.T) {
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	l := newLimiter(3, time.Minute)
	l.now = func() time.Time { return now }

	for i := range 3 {
		if ok, _ := l.allow("a"); !ok {
			t.Fatalf("попытка %d отклонена", i+1)
		}
	}
	ok, retry := l.allow("a")
	if ok || retry != time.Minute {
		t.Fatalf("четвёртая попытка: ok=%v retry=%v, ждали отказ на минуту", ok, retry)
	}
	if ok, _ := l.allow("b"); !ok {
		t.Fatal("другой ключ не должен страдать")
	}

	now = now.Add(30 * time.Second)
	if ok, retry := l.allow("a"); ok || retry != 30*time.Second {
		t.Fatalf("через полминуты: ok=%v retry=%v", ok, retry)
	}
	now = now.Add(30 * time.Second)
	if ok, _ := l.allow("a"); !ok {
		t.Fatal("через минуту попытка должна вернуться")
	}

	// полные вёдра выбрасываются, память не растёт
	now = now.Add(time.Hour)
	l.allow("c")
	if len(l.buckets) != 1 {
		t.Fatalf("после простоя осталось вёдер: %d", len(l.buckets))
	}
}

func TestClientIP(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "172.18.0.4:51234"
	r.Header.Set("X-Forwarded-For", "6.6.6.6, 1.2.3.4")

	if ip := (&Server{}).clientIP(r); ip != "172.18.0.4" {
		t.Fatalf("без TrustProxy заголовку верить нельзя: %s", ip)
	}
	// левые адреса мог подставить клиент — берём добавленный прокси
	if ip := (&Server{cfg: Config{TrustProxy: true}}).clientIP(r); ip != "1.2.3.4" {
		t.Fatalf("за прокси: %s", ip)
	}
}

func TestLoginRateLimited(t *testing.T) {
	ts := newTestServer(t)
	signup(t, ts.URL, "victim@tropa.kz")

	c := newClient(t, ts.URL)
	var resp *http.Response
	for range 10 {
		resp = c.post("/login", "/login", url.Values{"email": {"victim@tropa.kz"}, "password": {"wrong-pass"}})
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("неверный пароль до лимита: %d", resp.StatusCode)
		}
	}
	// одиннадцатая — уже не проверяется, даже с верным паролем
	resp = c.post("/login", "/login", url.Values{"email": {"victim@tropa.kz"}, "password": {"password123"}})
	if resp.StatusCode != http.StatusTooManyRequests || resp.Header.Get("Retry-After") == "" {
		t.Fatalf("после 10 попыток: %d, Retry-After=%q", resp.StatusCode, resp.Header.Get("Retry-After"))
	}
}

func TestRegisterRateLimited(t *testing.T) {
	ts := newTestServer(t)
	for i := range 5 {
		signup(t, ts.URL, "u"+strings.Repeat("x", i)+"@tropa.kz")
	}
	resp := newClient(t, ts.URL).post("/register", "/register", url.Values{
		"email": {"bot@tropa.kz"}, "name": {"Бот"}, "password": {"password123"}, "password2": {"password123"},
	})
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("шестая регистрация с одного адреса: %d", resp.StatusCode)
	}
}
