package dm

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"tropa/internal/auth"
	"tropa/internal/friends"
	"tropa/internal/store/storetest"
)

func setup(t *testing.T) (*Service, *friends.Service, func(name string) *auth.User) {
	db := storetest.New(t)
	a := auth.New(db)
	n := 0
	user := func(name string) *auth.User {
		n++
		u, err := a.Register(context.Background(), fmt.Sprintf("u%d@tropa.kz", n), name, "password123")
		if err != nil {
			t.Fatal(err)
		}
		// Telegram подключён — чтобы проверить уведомления
		if _, err := db.Exec(context.Background(), `UPDATE users SET telegram_chat_id = $2 WHERE id = $1`, u.ID, 500+n); err != nil {
			t.Fatal(err)
		}
		return u
	}
	s := New(db)
	s.SiteURL = "https://tropa.test"
	return s, friends.New(db), user
}

func befriend(t *testing.T, f *friends.Service, a, b *auth.User) {
	t.Helper()
	if _, err := f.Request(context.Background(), a, b.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.Accept(context.Background(), b, a.ID); err != nil {
		t.Fatal(err)
	}
}

func TestOnlyFriendsCanWrite(t *testing.T) {
	ctx := context.Background()
	s, f, user := setup(t)
	a, b := user("Аня"), user("Бекзат")
	if _, err := s.Post(ctx, a, b.ID, "привет"); !errors.Is(err, ErrNotFriends) {
		t.Fatalf("незнакомцу: %v", err)
	}
	// заявка ещё не принята — тоже нельзя
	if _, err := f.Request(ctx, a, b.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Post(ctx, a, b.ID, "привет"); !errors.Is(err, ErrNotFriends) {
		t.Fatalf("до принятия заявки: %v", err)
	}
	if err := f.Accept(ctx, b, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Post(ctx, a, b.ID, "  "); !errors.Is(err, ErrBadMessage) {
		t.Fatalf("пустое: %v", err)
	}
	if _, err := s.Post(ctx, a, b.ID, strings.Repeat("я", MaxMessage+1)); !errors.Is(err, ErrBadMessage) {
		t.Fatalf("длинное: %v", err)
	}
	if _, err := s.Post(ctx, a, b.ID, "привет"); err != nil {
		t.Fatal(err)
	}
	// удалили из друзей — писать больше нельзя
	if err := f.Remove(ctx, b.ID, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Post(ctx, b, a.ID, "ответ"); !errors.Is(err, ErrNotFriends) {
		t.Fatalf("после удаления: %v", err)
	}
}

func TestConversationAndUnread(t *testing.T) {
	ctx := context.Background()
	s, f, user := setup(t)
	a, b, c := user("Аня"), user("Бекзат"), user("Сауле")
	befriend(t, f, a, b)
	befriend(t, f, a, c)

	first, err := s.Post(ctx, a, b.ID, "привет")
	if err != nil {
		t.Fatal(err)
	}
	s.Post(ctx, b, a.ID, "салам")
	s.Post(ctx, b, a.ID, "как дела?")

	msgs, err := s.Conversation(ctx, a.ID, b.ID, 0)
	if err != nil || len(msgs) != 3 || msgs[0].Body != "привет" || msgs[2].Name != "Бекзат" {
		t.Fatalf("переписка: %+v %v", msgs, err)
	}
	if after, _ := s.Conversation(ctx, b.ID, a.ID, first.ID); len(after) != 2 {
		t.Fatalf("после id: %d", len(after))
	}
	if n, _ := s.Unread(ctx, a.ID); n != 2 {
		t.Fatalf("непрочитанных у Ани: %d", n)
	}
	if n, _ := s.Unread(ctx, b.ID); n != 1 {
		t.Fatalf("непрочитанных у Бекзата: %d", n)
	}

	threads, err := s.Threads(ctx, a.ID)
	if err != nil || len(threads) != 2 {
		t.Fatalf("переписки: %+v %v", threads, err)
	}
	// сначала те, с кем переписывались, потом остальные друзья
	if threads[0].UserID != b.ID || threads[0].Unread != 2 || threads[0].Last == nil || threads[0].Last.Body != "как дела?" {
		t.Fatalf("первая переписка: %+v", threads[0])
	}
	if threads[1].UserID != c.ID || threads[1].Last != nil {
		t.Fatalf("друг без переписки: %+v", threads[1])
	}

	if err := s.MarkRead(ctx, a.ID, b.ID); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.Unread(ctx, a.ID); n != 0 {
		t.Fatalf("после прочтения: %d", n)
	}
	if n, _ := s.Unread(ctx, b.ID); n != 1 {
		t.Fatalf("прочтение Ани не трогает Бекзата: %d", n)
	}
	if msgs, _ := s.Conversation(ctx, c.ID, b.ID, 0); len(msgs) != 0 {
		t.Fatalf("Сауле видит переписку Ани и Бекзата: %d", len(msgs))
	}

	// уведомление в Telegram — одно на окно в 15 минут, со ссылкой на ответ
	var n int
	var text string
	s.DB.QueryRow(ctx, `SELECT count(*), coalesce(max(text), '') FROM notifications WHERE user_id = $1 AND kind = 'dm'`, a.ID).Scan(&n, &text)
	if n != 1 || !strings.Contains(text, "Бекзат") || !strings.Contains(text, fmt.Sprintf("https://tropa.test/messages/%d", b.ID)) {
		t.Fatalf("уведомления Ане: %d %q", n, text)
	}
}
