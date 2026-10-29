package places

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"tropa/internal/store/storetest"
)

func addUser(t *testing.T, db *pgxpool.Pool, email, role string, chat int64) int64 {
	t.Helper()
	var id int64
	var tg *int64
	if chat != 0 {
		tg = &chat
	}
	if err := db.QueryRow(context.Background(), `INSERT INTO users (email, name, password_hash, role, telegram_chat_id)
		VALUES ($1, $1, 'x', $2, $3) RETURNING id`, email, role, tg).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func suggestion() *Place {
	return &Place{Name: "Антикафе «Время»", Category: Indoor, Kind: "anticafe",
		Lat: 43.2389, Lon: 76.9455, Address: "Абая 10", Description: "Настолки и чай", Indoor: true}
}

func notifications(t *testing.T, db *pgxpool.Pool, user int64, kind string) int {
	t.Helper()
	var n int
	db.QueryRow(context.Background(), `SELECT count(*) FROM notifications WHERE user_id = $1 AND kind = $2`, user, kind).Scan(&n)
	return n
}

func TestSuggestAndReview(t *testing.T) {
	ctx := context.Background()
	db := storetest.New(t)
	s := New(db)
	s.SiteURL = "https://tropa.test"
	author := addUser(t, db, "author@tropa.kz", "user", 101)
	admin := addUser(t, db, "admin@tropa.kz", "admin", 102)
	addUser(t, db, "admin2@tropa.kz", "admin", 0) // без Telegram — уведомления нет

	// ошибки полей: точка вне области, вид не из группы
	bad := suggestion()
	bad.Lat, bad.Lon, bad.Kind = 51.13, 71.43, "peak"
	errs, err := s.Suggest(ctx, author, "Автор", bad)
	if !errors.Is(err, ErrInvalid) || errs["coords"] == "" || errs["kind"] == "" || errs["slug"] != "" {
		t.Fatalf("неверное предложение: %v %v", errs, err)
	}

	p := suggestion()
	// пользователь не выбирает источник, статус и osm_ref
	p.Status, p.Source, p.OSMRef = Published, "osm", "node/1"
	if errs, err := s.Suggest(ctx, author, "Автор", p); err != nil {
		t.Fatalf("предложение: %v %v", errs, err)
	}
	if p.Slug != "antikafe-vremya" || p.Area != Almaty || p.Status != Draft {
		t.Fatalf("черновик: %+v", p)
	}
	if _, err := s.Get(ctx, p.Slug); !errors.Is(err, ErrNotFound) {
		t.Fatal("черновик виден на сайте")
	}
	if n := notifications(t, db, admin, "place_suggested"); n != 1 {
		t.Fatalf("админу уведомлений: %d", n)
	}

	// то же название — следующий свободный slug
	p2 := suggestion()
	if _, err := s.Suggest(ctx, author, "Автор", p2); err != nil || p2.Slug != "antikafe-vremya-2" {
		t.Fatalf("второй slug: %q %v", p2.Slug, err)
	}

	// черновик из seed-файла (без автора) — не для модерации
	seedDraft := valid()
	seedDraft.Status = Draft
	if _, err := s.Upsert(ctx, &seedDraft); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Publish(ctx, seedDraft.ID, nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("публикация черновика из seed: %v", err)
	}

	pending, err := s.Pending(ctx, 10)
	if err != nil || len(pending) != 2 || pending[0].ID != p.ID || pending[0].AuthorName != "author@tropa.kz" {
		t.Fatalf("очередь: %v %v", pending, err)
	}

	// публикация с правкой; неверная правка не публикует
	edit := suggestion()
	edit.Lat = 0
	if errs, err := s.Publish(ctx, p.ID, edit); !errors.Is(err, ErrInvalid) || errs["coords"] == "" {
		t.Fatalf("неверная правка: %v %v", errs, err)
	}
	edit = suggestion()
	edit.Name = "Антикафе Время"
	if _, err := s.Publish(ctx, p.ID, edit); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(ctx, p.Slug)
	if err != nil || got.Name != "Антикафе Время" {
		t.Fatalf("опубликованное: %+v %v", got, err)
	}
	if n := notifications(t, db, author, "place_published"); n != 1 {
		t.Fatalf("автору о публикации: %d", n)
	}
	if _, err := s.Publish(ctx, p.ID, nil); !errors.Is(err, ErrReviewed) {
		t.Fatalf("повторная публикация: %v", err)
	}

	// отказ: причина обязательна, автор видит её у себя
	if err := s.Reject(ctx, p2.ID, " "); !errors.Is(err, ErrInvalid) {
		t.Fatalf("отказ без причины: %v", err)
	}
	if err := s.Reject(ctx, p2.ID, "Дубль места выше"); err != nil {
		t.Fatal(err)
	}
	if err := s.Reject(ctx, p.ID, "поздно"); !errors.Is(err, ErrReviewed) {
		t.Fatalf("отказ опубликованного: %v", err)
	}
	mine, err := s.ByAuthor(ctx, author, 10)
	if err != nil || len(mine) != 2 || mine[0].Status != Rejected || mine[0].RejectReason != "Дубль места выше" || mine[1].Status != Published {
		t.Fatalf("мои предложения: %v %v", mine, err)
	}
	if n := notifications(t, db, author, "place_rejected"); n != 1 {
		t.Fatalf("автору об отказе: %d", n)
	}
	if _, err := s.PendingByID(ctx, p2.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("отклонённое в очереди: %v", err)
	}

	// seed не перезаписывает место пользователя с тем же slug
	seeded := valid()
	seeded.Slug = p.Slug
	if res, err := s.Upsert(ctx, &seeded); err != nil || res != Unchanged {
		t.Fatalf("seed поверх места пользователя: %v %v", res, err)
	}
	if got, _ := s.Get(ctx, p.Slug); got.Name != "Антикафе Время" {
		t.Fatalf("место пользователя перезаписано: %q", got.Name)
	}
}

func TestSuggestPendingLimit(t *testing.T) {
	ctx := context.Background()
	db := storetest.New(t)
	s := New(db)
	author := addUser(t, db, "author@tropa.kz", "user", 0)

	// формы отправлены одновременно — больше MaxPending всё равно не пройдёт
	var wg sync.WaitGroup
	results := make(chan error, MaxPending+5)
	for range MaxPending + 5 {
		wg.Go(func() {
			_, err := s.Suggest(ctx, author, "Автор", suggestion())
			results <- err
		})
	}
	wg.Wait()
	close(results)
	ok, limited := 0, 0
	for err := range results {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, ErrTooManyPending):
			limited++
		default:
			t.Fatal(err)
		}
	}
	if ok != MaxPending || limited != 5 {
		t.Fatalf("прошло %d, отказано %d", ok, limited)
	}
}
