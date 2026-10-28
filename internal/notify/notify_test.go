package notify

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"tropa/internal/auth"
	"tropa/internal/store/storetest"
)

// fakeTelegram — поддельный Bot API: запоминает отправленное, а ответ
// на sendMessage для чата задаётся через reply.
type fakeTelegram struct {
	mu    sync.Mutex
	sent  map[int64][]string
	reply map[int64]string // chat → тело ответа; нет — ok
	calls int
}

func newFake(t *testing.T) (*fakeTelegram, *Bot) {
	f := &fakeTelegram{sent: map[int64][]string{}, reply: map[int64]string{}}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.calls++
		if !strings.HasPrefix(r.URL.Path, "/botTEST:token/") {
			w.WriteHeader(404)
			io.WriteString(w, `{"ok":false,"error_code":404,"description":"Not Found"}`)
			return
		}
		switch strings.TrimPrefix(r.URL.Path, "/botTEST:token/") {
		case "getMe":
			io.WriteString(w, `{"ok":true,"result":{"username":"TropaTestBot"}}`)
		case "sendMessage":
			var m struct {
				ChatID int64  `json:"chat_id"`
				Text   string `json:"text"`
			}
			json.NewDecoder(r.Body).Decode(&m)
			if body, ok := f.reply[m.ChatID]; ok {
				w.WriteHeader(400)
				io.WriteString(w, body)
				return
			}
			f.sent[m.ChatID] = append(f.sent[m.ChatID], m.Text)
			io.WriteString(w, `{"ok":true,"result":{}}`)
		default:
			io.WriteString(w, `{"ok":true,"result":[]}`)
		}
	}))
	t.Cleanup(ts.Close)
	b := NewBot("TEST:token")
	b.BaseURL = ts.URL
	return f, b
}

func (f *fakeTelegram) messages(chat int64) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.sent[chat]...)
}

type env struct {
	s    *Service
	db   *pgxpool.Pool
	fake *fakeTelegram
	now  *time.Time
	auth *auth.Service
}

func setup(t *testing.T) *env {
	t.Helper()
	db := storetest.New(t)
	fake, bot := newFake(t)
	s := New(db, bot, slog.New(slog.NewTextHandler(io.Discard, nil)))
	now := time.Date(2027, 3, 1, 10, 0, 0, 0, time.UTC)
	s.Now = func() time.Time { return now }
	name, err := bot.Username(context.Background())
	if err != nil || name != "TropaTestBot" {
		t.Fatalf("getMe: %q %v", name, err)
	}
	s.BotName = name
	return &env{s: s, db: db, fake: fake, now: &now, auth: auth.New(db)}
}

func (e *env) user(t *testing.T, email string) *auth.User {
	t.Helper()
	u, err := e.auth.Register(context.Background(), email, "Аружан", "password123")
	if err != nil {
		t.Fatal(err)
	}
	return u
}

// start — личное сообщение боту из чата chat.
func start(chat int64, text string) Update {
	var u Update
	b, _ := json.Marshal(map[string]any{"update_id": 1, "message": map[string]any{
		"text": text, "chat": map[string]any{"id": chat, "type": "private"}}})
	json.Unmarshal(b, &u)
	return u
}

// link проводит пользователя через «Подключить Telegram» и /start <код>.
func (e *env) link(t *testing.T, u *auth.User, chat int64) {
	t.Helper()
	url, err := e.s.LinkURL(context.Background(), u.ID)
	if err != nil {
		t.Fatal(err)
	}
	code := strings.TrimPrefix(url, "https://t.me/TropaTestBot?start=")
	if code == url || len(code) != 24 {
		t.Fatalf("ссылка: %s", url)
	}
	if err := e.s.Handle(context.Background(), start(chat, "/start "+code)); err != nil {
		t.Fatal(err)
	}
}

func TestLink(t *testing.T) {
	ctx := context.Background()
	e := setup(t)
	u := e.user(t, "a@tropa.kz")

	e.link(t, u, 1001)
	if ok, _ := e.s.Linked(ctx, u.ID); !ok {
		t.Fatal("не привязан")
	}
	if msgs := e.fake.messages(1001); len(msgs) != 1 || !strings.Contains(msgs[0], "Аккаунт привязан") {
		t.Fatalf("ответ бота: %q", msgs)
	}

	// код одноразовый
	url, _ := e.s.LinkURL(ctx, u.ID)
	code := strings.TrimPrefix(url, "https://t.me/TropaTestBot?start=")
	e.s.Handle(ctx, start(2002, "/start "+code))
	e.s.Handle(ctx, start(3003, "/start "+code))
	if msgs := e.fake.messages(3003); len(msgs) != 1 || !strings.Contains(msgs[0], "устарела") {
		t.Fatalf("повтор кода: %q", msgs)
	}

	// просроченный код
	url, _ = e.s.LinkURL(ctx, u.ID)
	code = strings.TrimPrefix(url, "https://t.me/TropaTestBot?start=")
	*e.now = e.now.Add(LinkTTL + time.Second)
	if _, err := e.s.Link(ctx, code, 4004); !errors.Is(err, ErrBadLink) {
		t.Fatalf("просроченный код: %v", err)
	}
	if _, err := e.s.Link(ctx, "выдуманный", 4004); !errors.Is(err, ErrBadLink) {
		t.Fatalf("выдуманный код: %v", err)
	}

	// /stop отключает
	e.s.Handle(ctx, start(2002, "/stop"))
	if ok, _ := e.s.Linked(ctx, u.ID); ok {
		t.Fatal("после /stop остался привязан")
	}
	// /start без кода и прочий текст — подсказка
	e.s.Handle(ctx, start(5005, "привет"))
	if msgs := e.fake.messages(5005); len(msgs) != 1 || !strings.Contains(msgs[0], "Подключить Telegram") {
		t.Fatalf("подсказка: %q", msgs)
	}
}

func TestDispatch(t *testing.T) {
	ctx := context.Background()
	e := setup(t)
	a, b, c, nobody := e.user(t, "a@tropa.kz"), e.user(t, "b@tropa.kz"), e.user(t, "c@tropa.kz"), e.user(t, "n@tropa.kz")
	e.link(t, a, 1)
	e.link(t, b, 2)
	e.link(t, c, 3)
	e.fake.reply[2] = `{"ok":false,"error_code":403,"description":"Forbidden: bot was blocked by the user"}`
	e.fake.reply[3] = `{"ok":false,"error_code":429,"description":"Too Many Requests","parameters":{"retry_after":7}}`

	for _, u := range []*auth.User{a, b, c, nobody} {
		if err := Enqueue(ctx, e.db, u.ID, "test", "Место освободилось", ""); err != nil {
			t.Fatal(err)
		}
	}
	// повтор по ключу не ставится
	Enqueue(ctx, e.db, a.ID, "test", "Напоминание", "remind:1")
	Enqueue(ctx, e.db, a.ID, "test", "Напоминание", "remind:1")

	var queued int
	e.db.QueryRow(ctx, `SELECT count(*) FROM notifications`).Scan(&queued)
	if queued != 4 { // a×2, b, c; без Telegram — не ставится
		t.Fatalf("в очереди %d, ждали 4", queued)
	}

	n, err := e.s.Dispatch(ctx)
	if err != nil || n != 2 {
		t.Fatalf("отправлено %d (%v), ждали 2", n, err)
	}
	if msgs := e.fake.messages(1); len(msgs) != 3 { // «привязан» + два уведомления
		t.Fatalf("a получил %q", msgs)
	}
	// b заблокировал бота — отвязан, уведомление брошено
	if ok, _ := e.s.Linked(ctx, b.ID); ok {
		t.Fatal("b не отвязан после 403")
	}
	// c — 429: повтор через retry_after
	var next time.Time
	var attempts int
	e.db.QueryRow(ctx, `SELECT next_attempt_at, attempts FROM notifications WHERE user_id = $1`, c.ID).Scan(&next, &attempts)
	if !next.Equal(e.now.Add(7*time.Second)) || attempts != 1 {
		t.Fatalf("429: следующая попытка %v, попыток %d", next, attempts)
	}
	if n, _ := e.s.Dispatch(ctx); n != 0 {
		t.Fatalf("раньше срока отправлено %d", n)
	}
	delete(e.fake.reply, 3)
	*e.now = e.now.Add(8 * time.Second)
	if n, _ := e.s.Dispatch(ctx); n != 1 || len(e.fake.messages(3)) != 2 {
		t.Fatalf("повтор для c: %d", n)
	}
	if n, _ := e.s.Dispatch(ctx); n != 0 {
		t.Fatalf("отправлено повторно: %d", n)
	}
}

func TestDispatchGivesUp(t *testing.T) {
	ctx := context.Background()
	e := setup(t)
	u := e.user(t, "a@tropa.kz")
	e.link(t, u, 1)
	e.fake.reply[1] = `{"ok":false,"error_code":502,"description":"Bad Gateway"}`
	Enqueue(ctx, e.db, u.ID, "test", "текст", "")
	for i := 1; i <= MaxAttempts; i++ {
		e.s.Dispatch(ctx)
		*e.now = e.now.Add(time.Hour)
	}
	var failed bool
	e.db.QueryRow(ctx, `SELECT failed_at IS NOT NULL FROM notifications`).Scan(&failed)
	if !failed {
		t.Fatal("после MaxAttempts уведомление не брошено")
	}
	if ok, _ := e.s.Linked(ctx, u.ID); !ok {
		t.Fatal("5xx не должен отвязывать Telegram")
	}
}

func TestBackoff(t *testing.T) {
	for attempt, want := range map[int]time.Duration{1: 30 * time.Second, 2: time.Minute, 3: 2 * time.Minute, 20: time.Hour} {
		if got := Backoff(attempt); got != want {
			t.Errorf("Backoff(%d) = %v, ждали %v", attempt, got, want)
		}
	}
}
