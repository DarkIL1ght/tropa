package web

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"tropa/internal/notify"
)

var resetLinkRe = regexp.MustCompile(`https://tropa\.test(/password/reset/[A-Za-z0-9_-]+)`)

func TestPasswordResetFlow(t *testing.T) {
	var svc *notify.Service
	ts, db := newTestEnv(t, func(s *Server) {
		s.cfg.SiteURL = "https://tropa.test"
		svc = notify.New(nil, notify.NewBot("TEST:token"), slog.New(slog.NewTextHandler(io.Discard, nil)))
		svc.BotName = "TropaTestBot"
		s.notify = svc
	})
	svc.DB = db
	ctx := context.Background()
	signup(t, ts.URL, "tg@tropa.kz")
	signup(t, ts.URL, "notg@tropa.kz")
	if _, err := db.Exec(ctx, `UPDATE users SET telegram_chat_id = 42 WHERE email = 'tg@tropa.kz'`); err != nil {
		t.Fatal(err)
	}
	outbox := func() []string {
		rows, _ := db.Query(ctx, `SELECT text FROM notifications WHERE kind = 'password_reset' ORDER BY id`)
		var texts []string
		for rows.Next() {
			var s string
			rows.Scan(&s)
			texts = append(texts, s)
		}
		return texts
	}

	// ответ одинаковый: есть ли аккаунт и Telegram — не выдаём
	for _, email := range []string{"nobody@tropa.kz", "notg@tropa.kz", "TG@tropa.kz"} {
		c := newClient(t, ts.URL)
		_, page := c.get("/password/forgot")
		resp, err := c.http.PostForm(ts.URL+"/password/forgot", url.Values{"email": {email}, "csrf_token": {csrfRe.FindStringSubmatch(page)[1]}})
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "Проверьте Telegram") {
			t.Fatalf("%s: %d", email, resp.StatusCode)
		}
	}
	texts := outbox()
	if len(texts) != 1 {
		t.Fatalf("в очереди %d сообщений, ждали одно — для аккаунта с Telegram", len(texts))
	}
	m := resetLinkRe.FindStringSubmatch(texts[0])
	if m == nil {
		t.Fatalf("в сообщении нет ссылки: %q", texts[0])
	}
	link := m[1]

	c := newClient(t, ts.URL)
	if code, body := c.get(link); code != 200 || !strings.Contains(body, "Новый пароль") {
		t.Fatalf("страница сброса: %d", code)
	}
	resp := c.post(link, link, url.Values{"password": {"new-password"}, "password2": {"other-password"}})
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("разные пароли: %d", resp.StatusCode)
	}
	resp = c.post(link, link, url.Values{"password": {"new-password"}, "password2": {"new-password"}})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("сброс: %d", resp.StatusCode)
	}
	if code, body := c.get("/me"); code != 200 || !strings.Contains(body, "tg@tropa.kz") {
		t.Fatalf("после сброса не вошли: %d", code)
	}
	// ссылка одноразовая
	if code, body := newClient(t, ts.URL).get(link); code != http.StatusNotFound || !strings.Contains(body, "Ссылка не работает") {
		t.Fatalf("повторно: %d", code)
	}
	// новый пароль работает
	resp = newClient(t, ts.URL).post("/login", "/login", url.Values{"email": {"tg@tropa.kz"}, "password": {"new-password"}})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("вход с новым паролем: %d", resp.StatusCode)
	}
}
