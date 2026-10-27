// Tropa — платформа для организации походов.
//
//	tropa serve    — веб-сервер (миграции применяются при старте)
//	tropa migrate  — только применить миграции
//
// Настройки через переменные окружения:
//
//	DATABASE_URL    postgres://user:pass@host:5432/db (обязательно)
//	ADDR            адрес сервера, по умолчанию 127.0.0.1:8080
//	SECURE_COOKIES  1 — cookie только по HTTPS (за прокси с TLS)
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"tropa/internal/auth"
	"tropa/internal/store"
	"tropa/internal/trips"
	"tropa/internal/web"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	cmd := "serve"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var err error
	switch cmd {
	case "serve":
		err = serve(ctx, log)
	case "migrate":
		err = withDB(ctx, func(pool *pgxpool.Pool) error { return migrate(ctx, log, pool) })
	default:
		err = fmt.Errorf("неизвестная команда %q (есть: serve, migrate)", cmd)
	}
	if err != nil {
		log.Error(err.Error())
		os.Exit(1)
	}
}

func withDB(ctx context.Context, fn func(*pgxpool.Pool) error) error {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		return errors.New("не задан DATABASE_URL")
	}
	pool, err := store.Open(ctx, url)
	if err != nil {
		return err
	}
	defer pool.Close()
	return fn(pool)
}

func migrate(ctx context.Context, log *slog.Logger, pool *pgxpool.Pool) error {
	applied, err := store.Migrate(ctx, pool)
	for _, name := range applied {
		log.Info("миграция применена", "name", name)
	}
	return err
}

func serve(ctx context.Context, log *slog.Logger) error {
	return withDB(ctx, func(pool *pgxpool.Pool) error {
		if err := migrate(ctx, log, pool); err != nil {
			return err
		}
		authSvc := auth.New(pool)
		srv, err := web.New(web.Config{Secure: os.Getenv("SECURE_COOKIES") == "1"}, authSvc, trips.New(pool), log)
		if err != nil {
			return err
		}

		go purgeSessions(ctx, log, authSvc)

		addr := os.Getenv("ADDR")
		if addr == "" {
			addr = "127.0.0.1:8080"
		}
		hs := &http.Server{
			Addr:              addr,
			Handler:           srv.Handler(),
			ReadHeaderTimeout: 5 * time.Second,
			ReadTimeout:       15 * time.Second,
			WriteTimeout:      30 * time.Second,
			IdleTimeout:       2 * time.Minute,
		}
		errc := make(chan error, 1)
		go func() {
			log.Info("сервер запущен", "addr", "http://"+addr)
			errc <- hs.ListenAndServe()
		}()

		select {
		case err := <-errc:
			return err
		case <-ctx.Done():
		}
		log.Info("остановка сервера")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return hs.Shutdown(shutdownCtx)
	})
}

// purgeSessions раз в час удаляет просроченные сессии.
func purgeSessions(ctx context.Context, log *slog.Logger, a *auth.Service) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		if n, err := a.PurgeExpired(ctx); err != nil && ctx.Err() == nil {
			log.Warn("очистка сессий", "err", err)
		} else if n > 0 {
			log.Info("удалены просроченные сессии", "count", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
