package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"tropa/internal/store/storetest"
)

func TestPasswordReset(t *testing.T) {
	ctx := context.Background()
	svc := New(storetest.New(t))
	u, err := svc.Register(ctx, "a@b.kz", "Дархан", "old-password")
	if err != nil {
		t.Fatal(err)
	}
	session, _, _ := svc.CreateSession(ctx, u.ID)

	var token string
	capture := func(_ pgx.Tx, id int64, tok string) error {
		if id != u.ID {
			t.Fatalf("токен не тому пользователю: %d", id)
		}
		token = tok
		return nil
	}
	if found, err := svc.RequestReset(ctx, "nobody@b.kz", ResetTTL, capture); err != nil || found || token != "" {
		t.Fatalf("неизвестный email: found=%v err=%v", found, err)
	}
	if found, err := svc.RequestReset(ctx, "A@B.kz", ResetTTL, capture); err != nil || !found {
		t.Fatalf("запрос: found=%v err=%v", found, err)
	}
	old := token
	// второй запрос отменяет первый токен
	svc.RequestReset(ctx, "a@b.kz", ResetTTL, capture)
	if _, err := svc.ResetUser(ctx, old); !errors.Is(err, ErrBadReset) {
		t.Fatalf("старый токен жив: %v", err)
	}
	if got, err := svc.ResetUser(ctx, token); err != nil || got.ID != u.ID {
		t.Fatalf("владелец токена: %v", err)
	}

	if _, err := svc.ResetPassword(ctx, token, "short"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("короткий пароль: %v", err)
	}
	if id, err := svc.ResetPassword(ctx, token, "new-password"); err != nil || id != u.ID {
		t.Fatalf("сброс: id=%d err=%v", id, err)
	}
	if _, err := svc.Authenticate(ctx, "a@b.kz", "new-password"); err != nil {
		t.Fatalf("вход с новым паролем: %v", err)
	}
	if _, err := svc.UserBySession(ctx, session); !errors.Is(err, ErrNoSession) {
		t.Fatalf("старая сессия жива после сброса: %v", err)
	}
	if _, err := svc.ResetPassword(ctx, token, "other-password"); !errors.Is(err, ErrBadReset) {
		t.Fatalf("токен сработал дважды: %v", err)
	}

	// просроченный токен не работает
	svc.RequestReset(ctx, "a@b.kz", ResetTTL, capture)
	later := time.Now().Add(ResetTTL + time.Minute)
	svc.Now = func() time.Time { return later }
	if _, err := svc.ResetPassword(ctx, token, "other-password"); !errors.Is(err, ErrBadReset) {
		t.Fatalf("просроченный токен: %v", err)
	}
}
