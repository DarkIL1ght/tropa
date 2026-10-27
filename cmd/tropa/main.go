// Tropa — карта мест Алматы и области, куда сходить с друзьями, и встречи на ней.
//
//	tropa serve               — веб-сервер (миграции применяются при старте)
//	tropa migrate             — только применить миграции
//	tropa seed [папка]        — загрузить места из data/places/*.json (идемпотентно)
//	tropa make-admin <email>  — выдать роль админа зарегистрированному пользователю
//
// Настройки — переменные окружения или файл .env в корне проекта
// (образец — .env.example; заданные в окружении важнее файла):
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
	"path/filepath"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"tropa/internal/auth"
	"tropa/internal/places"
	"tropa/internal/store"
	"tropa/internal/trips"
	"tropa/internal/web"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if wd, err := os.Getwd(); err == nil {
		if p := findDotEnv(wd); p != "" {
			if err := loadDotEnv(p); err != nil {
				log.Error(err.Error())
				os.Exit(1)
			}
			log.Info("настройки из файла", "path", p)
		}
	}
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
	case "seed":
		err = seed(ctx, log, os.Args[2:])
	case "make-admin":
		if len(os.Args) != 3 {
			err = errors.New("использование: tropa make-admin <email>")
			break
		}
		err = withDB(ctx, func(pool *pgxpool.Pool) error {
			if err := migrate(ctx, log, pool); err != nil {
				return err
			}
			if err := auth.New(pool).SetRole(ctx, os.Args[2], "admin"); err != nil {
				return err
			}
			log.Info("роль админа выдана", "email", os.Args[2])
			return nil
		})
	default:
		err = fmt.Errorf("неизвестная команда %q (есть: serve, migrate, seed, make-admin)", cmd)
	}
	if err != nil {
		log.Error(err.Error())
		os.Exit(1)
	}
}

func withDB(ctx context.Context, fn func(*pgxpool.Pool) error) error {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		return errors.New("не задан DATABASE_URL: скопируйте .env.example в .env в корне проекта " +
			"и укажите адрес базы (или задайте переменную окружения)")
	}
	pool, err := store.Open(ctx, url)
	if err != nil {
		return err
	}
	defer pool.Close()
	return fn(pool)
}

// seed загружает места из папки (по умолчанию data/places в корне проекта).
// Сначала проверяются все файлы, потом всё пишется одной транзакцией.
func seed(ctx context.Context, log *slog.Logger, args []string) error {
	dir := ""
	if len(args) > 0 {
		dir = args[0]
	} else if wd, err := os.Getwd(); err == nil {
		dir = filepath.Join(moduleRoot(wd), "data", "places")
	}
	list, err := places.LoadSeed(os.DirFS(dir))
	if err != nil {
		return fmt.Errorf("%s: %w", dir, err)
	}
	return withDB(ctx, func(pool *pgxpool.Pool) error {
		if err := migrate(ctx, log, pool); err != nil {
			return err
		}
		st, err := places.New(pool).Seed(ctx, list)
		if err != nil {
			return err
		}
		log.Info("места загружены", "dir", dir, "total", len(list), "result", st.String())
		return nil
	})
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
		srv, err := web.New(web.Config{Secure: os.Getenv("SECURE_COOKIES") == "1"},
			web.Services{Auth: authSvc, Trips: trips.New(pool), Places: places.New(pool)}, log)
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
