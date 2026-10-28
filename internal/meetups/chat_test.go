package meetups

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"tropa/internal/auth"
)

func TestChat(t *testing.T) {
	ctx := context.Background()
	e := setup(t)
	e.s.SiteURL = "https://tropa.test"
	org, guest, queued, stranger := e.user(t), e.user(t), e.user(t), e.user(t)
	for i, u := range []*auth.User{org, guest, queued} {
		if _, err := e.db.Exec(ctx, `UPDATE users SET telegram_chat_id = $2 WHERE id = $1`, u.ID, 200+i); err != nil {
			t.Fatal(err)
		}
	}
	m := e.create(t, org, func(in *Input) { in.Capacity = "2" })
	e.s.Join(ctx, guest, m.ID)
	e.s.Join(ctx, queued, m.ID) // мест нет — очередь

	// писать могут идущие; очередь и посторонние — нет
	for _, u := range []*auth.User{queued, stranger} {
		if _, err := e.s.Post(ctx, m, u, "привет"); !errors.Is(err, ErrNoChat) {
			t.Fatalf("%s написал в чат: %v", u.Name, err)
		}
	}
	for _, body := range []string{"   ", strings.Repeat("я", MaxMessage+1)} {
		if _, err := e.s.Post(ctx, m, guest, body); !errors.Is(err, ErrBadMessage) {
			t.Fatalf("сообщение длины %d: %v", len([]rune(body)), err)
		}
	}
	first, err := e.s.Post(ctx, m, guest, "  Встречаемся у входа?  ")
	if err != nil || first.Body != "Встречаемся у входа?" || first.Name != guest.Name {
		t.Fatalf("сообщение: %+v %v", first, err)
	}
	second, _ := e.s.Post(ctx, m, org, "Да, в 10:00")

	all, err := e.s.Messages(ctx, m.ID, 0)
	if err != nil || len(all) != 2 || all[0].ID != first.ID || all[1].ID != second.ID {
		t.Fatalf("история: %+v %v", all, err)
	}
	if after, _ := e.s.Messages(ctx, m.ID, first.ID); len(after) != 1 || after[0].ID != second.ID {
		t.Fatalf("после первого: %+v", after)
	}

	// уведомления: организатору — о сообщении гостя, гостю — об ответе;
	// очереди — ничего, автору — ничего о своём
	if text := e.queued(t, org)["chat"]; !strings.Contains(text, "Встречаемся у входа?") || !strings.Contains(text, "https://tropa.test/m/"+m.Token+"#chat") {
		t.Fatalf("уведомление организатору: %q", text)
	}
	if text := e.queued(t, guest)["chat"]; !strings.Contains(text, "Да, в 10:00") {
		t.Fatalf("уведомление гостю: %q", text)
	}
	if text := e.queued(t, queued)["chat"]; text != "" {
		t.Fatalf("очереди пришло: %q", text)
	}

	// следующее сообщение в те же 15 минут не будит Telegram ещё раз
	e.s.Post(ctx, m, guest, "Беру термос")
	if text := e.queued(t, org)["chat"]; strings.Contains(text, "термос") {
		t.Fatal("второе уведомление за 15 минут")
	}
	*e.now = e.now.Add(chatPingEvery + time.Minute)
	e.s.Post(ctx, m, guest, "Я на месте")
	if text := e.queued(t, org)["chat"]; !strings.Contains(text, "Я на месте") {
		t.Fatal("после паузы уведомление не пришло")
	}

	// гость вышел — писать больше не может
	if err := e.s.Leave(ctx, guest, m.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.Post(ctx, m, guest, "ещё тут?"); !errors.Is(err, ErrNoChat) {
		t.Fatalf("вышедший написал: %v", err)
	}
}
