package web

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
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

const (
	ipA     = "203.0.113.1"
	ipB     = "198.51.100.2"
	victimV = "victim@tropa.kz"
	otherW  = "other@tropa.kz"
)

// forwardedFor — транспорт браузера за прокси: адрес посетителя в X-Forwarded-For.
type forwardedFor string

func (xff forwardedFor) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("X-Forwarded-For", string(xff))
	return http.DefaultTransport.RoundTrip(r)
}

// loginEnv — сервер за прокси с настоящими вёдрами входа; advance двигает
// только их часы: IP-ведро наполняется быстрее и перестаёт маскировать пару.
func loginEnv(t *testing.T) (ts *httptest.Server, advance func(time.Duration)) {
	start := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	var passed atomic.Int64
	ts, _ = newTestEnv(t, func(s *Server) {
		s.cfg.TrustProxy = true
		clock := func() time.Time { return start.Add(time.Duration(passed.Load())) }
		s.limits.loginIP.now = clock
		s.limits.loginPair.now = clock
	})
	signup(t, ts.URL, victimV)
	signup(t, ts.URL, otherW)
	return ts, func(d time.Duration) { passed.Add(int64(d)) }
}

// loginFrom — попытка входа новым браузером с заголовком X-Forwarded-For.
func loginFrom(t *testing.T, ts *httptest.Server, xff, email, password string) *http.Response {
	t.Helper()
	c := newClient(t, ts.URL)
	c.http.Transport = forwardedFor(xff)
	return c.post("/login", "/login", url.Values{"email": {email}, "password": {password}})
}

// exhaustPair тратит все попытки пары: левые адреса в заголовке клиент может
// менять как угодно, в счёт идёт только добавленный прокси.
func exhaustPair(t *testing.T, ts *httptest.Server, ip, email string) {
	t.Helper()
	for i := range 10 {
		xff := fmt.Sprintf("10.9.9.%d, %s", i, ip)
		if resp := loginFrom(t, ts, xff, email, "wrong-pass"); resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("попытка %d: %d, ждали 401", i+1, resp.StatusCode)
		}
	}
}

func TestLoginPairExhausted(t *testing.T) {
	ts, advance := loginEnv(t)
	exhaustPair(t, ts, ipA, victimV)

	// за минуту IP-ведро наполнилось, а у пары попытка ещё не появилась
	advance(time.Minute)
	resp := loginFrom(t, ts, ipA, strings.ToUpper(victimV), "password123")
	if resp.StatusCode != http.StatusTooManyRequests || resp.Header.Get("Retry-After") == "" {
		t.Fatalf("пара исчерпана: %d, Retry-After=%q", resp.StatusCode, resp.Header.Get("Retry-After"))
	}
}

func TestLoginSameEmailFromOtherIP(t *testing.T) {
	ts, _ := loginEnv(t)
	exhaustPair(t, ts, ipA, victimV)

	if resp := loginFrom(t, ts, ipB, victimV, "password123"); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("владелец с другого адреса: %d, ждали вход", resp.StatusCode)
	}
}

func TestLoginOtherEmailFromSameIP(t *testing.T) {
	ts, advance := loginEnv(t)
	exhaustPair(t, ts, ipA, victimV)

	advance(time.Minute)
	if resp := loginFrom(t, ts, ipA, otherW, "password123"); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("другой email с того же адреса: %d, ждали вход", resp.StatusCode)
	}
}

func TestLoginBruteForceFromOneIP(t *testing.T) {
	ts, _ := loginEnv(t)
	for i := range 10 {
		email := fmt.Sprintf("guess%d@tropa.kz", i)
		if resp := loginFrom(t, ts, ipA, email, "wrong-pass"); resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("попытка %d: %d, ждали 401", i+1, resp.StatusCode)
		}
	}
	// у пары попытки есть, но с этого адреса их уже слишком много
	if resp := loginFrom(t, ts, ipA, victimV, "password123"); resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("перебор с одного адреса: %d, ждали 429", resp.StatusCode)
	}
}

func TestLoginKey(t *testing.T) {
	if loginKey(ipA, "A@B.kz") != loginKey(ipA, "a@b.kz") {
		t.Fatal("регистр email не должен влиять на ключ")
	}
	// email из формы может содержать что угодно, в IPv6 есть двоеточия
	pairs := [][2]string{
		{"1.1.1.1", "a@b"}, {"1.1.1.1a", "@b"}, {"1.1.1", ".1a@b"},
		{"::1", "a@b"}, {"::", "1a@b"}, {":", ":1a@b"},
		{"1", "2|a"}, {"1|2", "a"}, {"1", "2\x00a"}, {"1\x002", "a"},
		{"1", "3:abc"}, {"3:a", "bc"}, {"", "1:ab"}, {"1:a", "b"},
	}
	seen := map[string][2]string{}
	for _, p := range pairs {
		key := loginKey(p[0], p[1])
		if prev, dup := seen[key]; dup {
			t.Fatalf("склейка пар %q и %q: ключ %q", prev, p, key)
		}
		seen[key] = p
	}
}
