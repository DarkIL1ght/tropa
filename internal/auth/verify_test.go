package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	"tropa/internal/store/storetest"
)

func TestEmailVerification(t *testing.T) {
	ctx := context.Background()
	svc := New(storetest.New(t))
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	svc.Now = func() time.Time { return now }
	u, err := svc.Register(ctx, "a@b.kz", "Дархан", "password123")
	if err != nil || u.Verified {
		t.Fatalf("новый аккаунт: %+v %v", u, err)
	}

	var code string
	capture := func(c string) error { code = c; return nil }
	if err := svc.SendCode(ctx, u, capture); err != nil || len(code) != 6 {
		t.Fatalf("код: %q %v", code, err)
	}
	if err := svc.SendCode(ctx, u, capture); !errors.Is(err, ErrCodeTooSoon) {
		t.Fatalf("повтор сразу: %v", err)
	}
	// письмо не ушло — новый код не сохраняется, прежний действует
	now = now.Add(CodeResend)
	old := code
	if err := svc.SendCode(ctx, u, func(string) error { return errors.New("smtp down") }); err == nil {
		t.Fatal("ошибка отправки потерялась")
	}
	if err := svc.VerifyEmail(ctx, u.ID, old); err != nil {
		t.Fatalf("прежний код после неудачной отправки: %v", err)
	}
	if got, _ := svc.UserByID(ctx, u.ID); !got.Verified {
		t.Fatal("почта не подтверждена")
	}
	// u — устаревшая копия без отметки: проверка идёт по базе
	if err := svc.SendCode(ctx, u, capture); !errors.Is(err, ErrVerified) {
		t.Fatalf("код подтверждённому: %v", err)
	}
}

func TestVerifyAttemptsAndExpiry(t *testing.T) {
	ctx := context.Background()
	svc := New(storetest.New(t))
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	svc.Now = func() time.Time { return now }
	u, _ := svc.Register(ctx, "a@b.kz", "Дархан", "password123")
	var code string
	capture := func(c string) error { code = c; return nil }
	svc.SendCode(ctx, u, capture)

	wrong := "000000"
	if code == wrong {
		wrong = "111111"
	}
	for i := range CodeAttempts {
		if err := svc.VerifyEmail(ctx, u.ID, wrong); !errors.Is(err, ErrBadCode) {
			t.Fatalf("попытка %d: %v", i, err)
		}
	}
	// после лимита ошибок не проходит даже верный код
	if err := svc.VerifyEmail(ctx, u.ID, code); !errors.Is(err, ErrCodeAttempts) {
		t.Fatalf("после лимита: %v", err)
	}

	now = now.Add(CodeResend)
	svc.SendCode(ctx, u, capture)
	now = now.Add(CodeTTL)
	if err := svc.VerifyEmail(ctx, u.ID, code); !errors.Is(err, ErrCodeExpired) {
		t.Fatalf("просроченный код: %v", err)
	}
	svc.SendCode(ctx, u, capture)
	// пробел посередине, как часто вставляют из письма, не мешает
	if err := svc.VerifyEmail(ctx, u.ID, code[:3]+" "+code[3:]); err != nil {
		t.Fatalf("код с пробелом: %v", err)
	}
	if err := svc.VerifyEmail(ctx, u.ID, code); !errors.Is(err, ErrCodeExpired) {
		t.Fatalf("код второй раз: %v", err)
	}
}

func TestPurgeUnverified(t *testing.T) {
	ctx := context.Background()
	svc := New(storetest.New(t))
	fresh, _ := svc.Register(ctx, "fresh@b.kz", "Новый", "password123")
	stale, _ := svc.Register(ctx, "stale@b.kz", "Старый", "password123")
	done, _ := svc.Register(ctx, "done@b.kz", "Подтвердил", "password123")
	svc.DB.Exec(ctx, `UPDATE users SET created_at = now() - interval '8 days' WHERE id = ANY($1)`, []int64{stale.ID, done.ID})
	svc.DB.Exec(ctx, `UPDATE users SET email_verified_at = now() WHERE id = $1`, done.ID)
	if n, err := svc.PurgeUnverified(ctx); err != nil || n != 1 {
		t.Fatalf("удалено %d: %v", n, err)
	}
	if _, err := svc.UserByID(ctx, stale.ID); !errors.Is(err, ErrNoUser) {
		t.Fatalf("старый неподтверждённый остался: %v", err)
	}
	for _, u := range []*User{fresh, done} {
		if _, err := svc.UserByID(ctx, u.ID); err != nil {
			t.Fatalf("%s удалён: %v", u.Email, err)
		}
	}
}
