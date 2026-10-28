package web

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"tropa/internal/notify"
)

func TestTelegramProfile(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"ok":true,"result":{"username":"TropaTestBot"}}`)
	}))
	defer api.Close()
	var svc *notify.Service
	ts, db := newTestEnv(t, func(s *Server) {
		bot := notify.NewBot("TEST:token")
		bot.BaseURL = api.URL
		svc = notify.New(nil, bot, slog.New(slog.NewTextHandler(io.Discard, nil)))
		svc.BotName = "TropaTestBot"
		s.notify = svc
	})
	svc.DB = db

	if code, body := newClient(t, ts.URL).get("/help/telegram"); code != 200 || !strings.Contains(body, "@TropaTestBot") {
		t.Fatalf("инструкция: %d", code)
	}
	c := signup(t, ts.URL, "a@tropa.kz")
	if _, body := c.get("/me"); !strings.Contains(body, "Подключить Telegram") {
		t.Fatal("в профиле нет кнопки подключения")
	}
	_, page := c.get("/me")
	resp, err := c.http.PostForm(ts.URL+"/me/telegram", url.Values{"csrf_token": {csrfRe.FindStringSubmatch(page)[1]}})
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	m := regexp.MustCompile(`href="https://t\.me/TropaTestBot\?start=([A-Za-z0-9_-]{24})"`).FindSubmatch(body)
	if resp.StatusCode != 200 || m == nil {
		t.Fatalf("страница подключения: %d", resp.StatusCode)
	}
	if _, err := svc.Link(context.Background(), string(m[1]), 777); err != nil {
		t.Fatal(err)
	}
	if _, body := c.get("/me"); !strings.Contains(body, "Telegram подключён") {
		t.Fatal("после привязки профиль не изменился")
	}
	c.post("/me", "/me/telegram/off", url.Values{})
	if _, body := c.get("/me"); !strings.Contains(body, "Подключить Telegram") {
		t.Fatal("не отключился")
	}
}

func TestTelegramDisabled(t *testing.T) {
	ts := newTestServer(t)
	c := signup(t, ts.URL, "a@tropa.kz")
	if _, body := c.get("/me"); strings.Contains(body, `class="card telegram"`) {
		t.Fatal("без бота блока Telegram в профиле быть не должно")
	}
	if resp := c.post("/me", "/me/telegram", url.Values{}); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("подключение без бота: %d", resp.StatusCode)
	}
	if code, body := c.get("/help/telegram"); code != 200 || !strings.Contains(body, "выключены") {
		t.Fatalf("инструкция без бота: %d", code)
	}
}
