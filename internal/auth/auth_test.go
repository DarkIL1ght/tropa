package auth

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"tropa/internal/store/storetest"
)

func TestValidateRegistration(t *testing.T) {
	cases := []struct {
		name, email, uname, pass string
		bad                      []string
	}{
		{"ok", "a@b.kz", "Дархан", "секретный1", nil},
		{"email", "not-an-email", "Дархан", "password1", []string{"email"}},
		{"email с именем", "Dar <a@b.kz>", "Дархан", "password1", []string{"email"}},
		{"короткое имя", "a@b.kz", "Д", "password1", []string{"name"}},
		{"короткий пароль", "a@b.kz", "Дархан", "1234567", []string{"password"}},
		{"пароль > 72 байт", "a@b.kz", "Дархан", strings.Repeat("ж", 40), []string{"password"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			errs := ValidateRegistration(c.email, c.uname, c.pass)
			if len(errs) != len(c.bad) {
				t.Fatalf("ошибки %v, ждали поля %v", errs, c.bad)
			}
			for _, f := range c.bad {
				if errs[f] == "" {
					t.Errorf("нет ошибки для поля %s: %v", f, errs)
				}
			}
		})
	}
}

func TestSafeNext(t *testing.T) {
	for in, want := range map[string]string{
		"/trips/5?tab=gear":   "/trips/5?tab=gear",
		"":                    "/",
		"https://evil.com":    "/",
		"//evil.com":          "/",
		"/\\evil.com":         "/",
		"javascript:alert(1)": "/",
	} {
		if got := SafeNext(in); got != want {
			t.Errorf("SafeNext(%q) = %q, ждали %q", in, got, want)
		}
	}
}

func TestRegisterLoginSession(t *testing.T) {
	ctx := context.Background()
	svc := New(storetest.New(t))

	u, err := svc.Register(ctx, "Darhan@Example.kz", "Дархан", "password123")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Register(ctx, "darhan@example.kz", "Другой", "password123"); !errors.Is(err, ErrEmailTaken) {
		t.Fatalf("повторный email без учёта регистра: %v", err)
	}

	if _, err := svc.Authenticate(ctx, "darhan@example.kz", "wrong-pass"); !errors.Is(err, ErrBadLogin) {
		t.Fatalf("неверный пароль: %v", err)
	}
	if _, err := svc.Authenticate(ctx, "nobody@example.kz", "password123"); !errors.Is(err, ErrBadLogin) {
		t.Fatalf("неизвестный email: %v", err)
	}
	got, err := svc.Authenticate(ctx, "DARHAN@example.kz", "password123")
	if err != nil || got.ID != u.ID {
		t.Fatalf("вход: %v %+v", err, got)
	}

	token, _, err := svc.CreateSession(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if s, err := svc.UserBySession(ctx, token); err != nil || s.ID != u.ID {
		t.Fatalf("сессия: %v %+v", err, s)
	}

	// через 31 день сессия просрочена и удаляется очисткой
	later := time.Now().Add(SessionTTL + 24*time.Hour)
	svc.Now = func() time.Time { return later }
	if _, err := svc.UserBySession(ctx, token); !errors.Is(err, ErrNoSession) {
		t.Fatalf("просроченная сессия: %v", err)
	}
	if n, err := svc.PurgeExpired(ctx); err != nil || n != 1 {
		t.Fatalf("очистка: n=%d err=%v", n, err)
	}

	svc.Now = time.Now
	token, _, _ = svc.CreateSession(ctx, u.ID)
	if err := svc.DeleteSession(ctx, token); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.UserBySession(ctx, token); !errors.Is(err, ErrNoSession) {
		t.Fatalf("после выхода: %v", err)
	}
}

func TestSetRole(t *testing.T) {
	ctx := context.Background()
	svc := New(storetest.New(t))
	if _, err := svc.Register(ctx, "boss@tropa.kz", "Админ", "password123"); err != nil {
		t.Fatal(err)
	}
	if err := svc.SetRole(ctx, "BOSS@tropa.kz", "admin"); err != nil {
		t.Fatal(err)
	}
	u, err := svc.Authenticate(ctx, "boss@tropa.kz", "password123")
	if err != nil || !u.IsAdmin() {
		t.Fatalf("роль не выдана: %+v %v", u, err)
	}
	if err := svc.SetRole(ctx, "nobody@tropa.kz", "admin"); err == nil {
		t.Fatal("несуществующий пользователь")
	}
	if err := svc.SetRole(ctx, "boss@tropa.kz", "god"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("неизвестная роль: %v", err)
	}
}

func TestProfileChanges(t *testing.T) {
	ctx := context.Background()
	svc := New(storetest.New(t))
	u, err := svc.Register(ctx, "a@tropa.kz", "Дархан", "password123")
	if err != nil {
		t.Fatal(err)
	}

	if err := svc.Rename(ctx, u.ID, "  Д  "); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("короткое имя: %v", err)
	}
	if err := svc.Rename(ctx, u.ID, "  Дархан Б. "); err != nil {
		t.Fatal(err)
	}
	if got, err := svc.UserByID(ctx, u.ID); err != nil || got.Name != "Дархан Б." {
		t.Fatalf("новое имя: %+v %v", got, err)
	}
	if _, err := svc.UserByID(ctx, u.ID+100); !errors.Is(err, ErrNoUser) {
		t.Fatalf("нет пользователя: %v", err)
	}

	phone, _, _ := svc.CreateSession(ctx, u.ID)
	laptop, _, _ := svc.CreateSession(ctx, u.ID)
	if err := svc.ChangePassword(ctx, u.ID, "wrong-pass", "new-password1"); !errors.Is(err, ErrBadPassword) {
		t.Fatalf("неверный текущий пароль: %v", err)
	}
	if err := svc.ChangePassword(ctx, u.ID, "password123", "short"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("короткий новый пароль: %v", err)
	}
	if _, err := svc.UserBySession(ctx, phone); err != nil {
		t.Fatalf("неудачная смена закрыла сессию: %v", err)
	}
	if err := svc.ChangePassword(ctx, u.ID, "password123", "new-password1"); err != nil {
		t.Fatal(err)
	}
	for _, tok := range []string{phone, laptop} {
		if _, err := svc.UserBySession(ctx, tok); !errors.Is(err, ErrNoSession) {
			t.Fatalf("после смены пароля сессия жива: %v", err)
		}
	}
	if _, err := svc.Authenticate(ctx, "a@tropa.kz", "password123"); !errors.Is(err, ErrBadLogin) {
		t.Fatalf("старый пароль подходит: %v", err)
	}
	if _, err := svc.Authenticate(ctx, "a@tropa.kz", "new-password1"); err != nil {
		t.Fatalf("новый пароль: %v", err)
	}
}
