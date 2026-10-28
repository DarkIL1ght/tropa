package friends

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"tropa/internal/auth"
	"tropa/internal/store/storetest"
)

func setup(t *testing.T) (*Service, func(name string) *auth.User) {
	db := storetest.New(t)
	a := auth.New(db)
	s := New(db)
	s.SiteURL = "https://tropa.test"
	n := 0
	user := func(name string) *auth.User {
		n++
		u, err := a.Register(context.Background(), fmt.Sprintf("u%d@tropa.kz", n), name, "password123")
		if err != nil {
			t.Fatal(err)
		}
		// Telegram подключён — чтобы проверить уведомления
		if _, err := db.Exec(context.Background(), `UPDATE users SET telegram_chat_id = $2 WHERE id = $1`, u.ID, 300+n); err != nil {
			t.Fatal(err)
		}
		return u
	}
	return s, user
}

func (s *Service) queued(t *testing.T, userID int64, kind string) string {
	t.Helper()
	var text string
	s.DB.QueryRow(context.Background(), `SELECT coalesce(string_agg(text, '|'), '') FROM notifications WHERE user_id = $1 AND kind = $2`, userID, kind).Scan(&text)
	return text
}

func TestFriendship(t *testing.T) {
	ctx := context.Background()
	s, user := setup(t)
	dar, aru, bek := user("Дархан"), user("Аружан"), user("Бекжан")

	if _, err := s.Request(ctx, dar, dar.ID); !errors.Is(err, ErrSelf) {
		t.Fatalf("сам себе: %v", err)
	}
	if _, err := s.Request(ctx, dar, 999999); !errors.Is(err, ErrNoUser) {
		t.Fatalf("несуществующий: %v", err)
	}

	st, err := s.Request(ctx, dar, aru.ID)
	if err != nil || st != Outgoing {
		t.Fatalf("заявка: %v %v", st, err)
	}
	if st, _ := s.Request(ctx, dar, aru.ID); st != Outgoing {
		t.Fatalf("повторная заявка: %v", st)
	}
	if text := s.queued(t, aru.ID, "friend_request"); strings.Count(text, "Дархан хочет добавить") != 1 || !strings.Contains(text, "https://tropa.test/friends") {
		t.Fatalf("уведомление о заявке (одно): %q", text)
	}
	if st, _ := s.Status(ctx, aru.ID, dar.ID); st != Incoming {
		t.Fatalf("у получателя: %v", st)
	}
	// чужую заявку принять за другого нельзя
	if err := s.Accept(ctx, bek, dar.ID); !errors.Is(err, ErrNoInvite) {
		t.Fatalf("принял не тот: %v", err)
	}
	if err := s.Accept(ctx, aru, dar.ID); err != nil {
		t.Fatal(err)
	}
	if st, _ := s.Status(ctx, dar.ID, aru.ID); st != Friends {
		t.Fatalf("после принятия: %v", st)
	}
	if text := s.queued(t, dar.ID, "friend_accepted"); !strings.Contains(text, "Аружан теперь у вас в друзьях") {
		t.Fatalf("уведомление о принятии: %q", text)
	}

	// встречная заявка = согласие
	s.Request(ctx, bek, dar.ID)
	if st, _ := s.Request(ctx, dar, bek.ID); st != Friends {
		t.Fatalf("встречная заявка: %v", st)
	}

	l, err := s.List(ctx, dar.ID)
	if err != nil || len(l.Friends) != 2 || l.Friends[0].Name != "Аружан" || len(l.Incoming)+len(l.Outgoing) != 0 {
		t.Fatalf("список: %+v %v", l, err)
	}
	ids, _ := s.IDs(ctx, dar.ID)
	if !ids[aru.ID] || !ids[bek.ID] || len(ids) != 2 {
		t.Fatalf("id друзей: %v", ids)
	}
	if f, in, _ := s.Counts(ctx, dar.ID); f != 2 || in != 0 {
		t.Fatalf("счётчики: %d %d", f, in)
	}

	// удалить может любой из двух
	if err := s.Remove(ctx, aru.ID, dar.ID); err != nil {
		t.Fatal(err)
	}
	if st, _ := s.Status(ctx, dar.ID, aru.ID); st != None {
		t.Fatalf("после удаления: %v", st)
	}
}

// Две встречные заявки одновременно — одна строка и дружба, без ошибки уникальности.
func TestFriendshipConcurrentRequests(t *testing.T) {
	ctx := context.Background()
	s, user := setup(t)
	for range 10 {
		a, b := user("Аня"), user("Боря")
		var wg sync.WaitGroup
		errs := make([]error, 2)
		for i, pair := range [][2]*auth.User{{a, b}, {b, a}} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, errs[i] = s.Request(ctx, pair[0], pair[1].ID)
			}()
		}
		wg.Wait()
		if errs[0] != nil || errs[1] != nil {
			t.Fatalf("ошибки: %v", errs)
		}
		if st, _ := s.Status(ctx, a.ID, b.ID); st != Friends {
			t.Fatalf("встречные заявки разом: %v", st)
		}
	}
}
