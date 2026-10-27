// Package storetest даёт тестам чистую базу: отдельную схему
// со всеми миграциями, которая удаляется после теста.
package storetest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"tropa/internal/store"
)

// EnvURL — переменная с адресом тестовой базы. Без неё тесты с базой пропускаются.
const EnvURL = "TROPA_TEST_DATABASE_URL"

// New возвращает пул, у которого search_path указывает на новую схему.
func New(t testing.TB) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv(EnvURL)
	if url == "" {
		t.Skipf("нет %s — тест с базой пропущен", EnvURL)
	}
	ctx := context.Background()

	b := make([]byte, 6)
	rand.Read(b)
	schema := "t_" + hex.EncodeToString(b)

	admin, err := store.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		admin.Close()
		t.Fatal(err)
	}

	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	cfg.MaxConns = 60 // конкурентные тесты записи открывают много транзакций
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
		admin.Close()
	})
	if _, err := store.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	return pool
}
