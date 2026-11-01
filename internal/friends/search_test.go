package friends

import (
	"context"
	"testing"
)

func TestSearch(t *testing.T) {
	ctx := context.Background()
	s, user := setup(t)
	me := user("Дархан")           // u1
	arman := user("Arman Petrov")  // u2
	armanS := user("Арман Сейтов") // u3
	bulan := user("Bulan")         // u4
	user("Arman Hidden")           // u5: почта не подтверждена — в поиске не виден
	for _, id := range []int64{me.ID, arman.ID, armanS.ID, bulan.ID} {
		if _, err := s.DB.Exec(ctx, `UPDATE users SET email_verified_at = now() WHERE id = $1`, id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Request(ctx, me, arman.ID); err != nil {
		t.Fatal(err)
	}

	names := func(q string) map[string]Status {
		t.Helper()
		found, err := s.Search(ctx, me.ID, q)
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]Status{}
		for _, f := range found {
			out[f.Name] = f.Status
		}
		return out
	}
	if got := names("arm"); len(got) != 1 || got["Arman Petrov"] != Outgoing {
		t.Fatalf("по части имени: %v", got)
	}
	if got := names("арман"); len(got) != 1 || got["Арман Сейтов"] != None {
		t.Fatalf("кириллица без учёта регистра: %v", got)
	}
	// email — только целиком и без учёта регистра; часть адреса ничего не находит
	if got := names("U4@Tropa.kz"); len(got) != 1 || got["Bulan"] != None {
		t.Fatalf("по email: %v", got)
	}
	if got := names("tropa.kz"); len(got) != 0 {
		t.Fatalf("часть email нашла людей: %v", got)
	}
	if got := names("Дархан"); len(got) != 0 {
		t.Fatalf("себя показывать не нужно: %v", got)
	}
	if got := names("a"); len(got) != 0 {
		t.Fatalf("запрос из одной буквы: %v", got)
	}
	if got := names("_%"); len(got) != 0 {
		t.Fatalf("%% и _ должны искаться буквально: %v", got)
	}
}
