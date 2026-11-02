// Tropa — карта мест Алматы и области, куда сходить с друзьями, и встречи на ней.
//
//	tropa serve               — веб-сервер (миграции применяются при старте)
//	tropa migrate             — только применить миграции
//	tropa seed [папка]        — загрузить места из data/places/*.json (идемпотентно)
//	tropa make-admin <email>  — выдать роль админа зарегистрированному пользователю
//	tropa reset-link <email>  — ссылка сброса пароля на сутки (для тех, у кого нет Telegram)
//	tropa osm-fetch [--from raw.json]
//	                          — кандидаты из OpenStreetMap в data/places/candidates.json
//	                            (базу не трогает; сырой ответ кэшируется во временной папке)
//
// Настройки — переменные окружения или файл .env в корне проекта
// (образец — .env.example; заданные в окружении важнее файла):
//
//	DATABASE_URL    postgres://user:pass@host:5432/db (обязательно)
//	ADDR            адрес сервера, по умолчанию 127.0.0.1:8080
//	SECURE_COOKIES  1 — cookie только по HTTPS (за прокси с TLS)
//	TRUST_PROXY     1 — адрес посетителя из X-Forwarded-For (только за своим прокси:
//	                иначе клиент подставит любой адрес и обойдёт ограничения попыток)
//	WEATHER_URL     API погоды, по умолчанию Open-Meteo; off — без плашки погоды
//	SITE_URL        адрес сайта для ссылок в уведомлениях, по умолчанию http://ADDR
//	TELEGRAM_BOT_TOKEN
//	                токен бота уведомлений (docs/telegram.md); пусто — без уведомлений
//	TELEGRAM_API_URL  адрес Bot API, по умолчанию https://api.telegram.org
//	RESEND_API_KEY  ключ resend.com для писем (коды подтверждения); пусто — письма только в лог
//	MAIL_FROM       отправитель, по умолчанию «Tropa <onboarding@resend.dev>» (без своего домена
//	                Resend доставляет только на адрес владельца аккаунта)
//	EMAIL_VERIFICATION
//	                1 — без подтверждённой почты аккаунт только смотрит; включать, когда письма
//	                доходят до всех (свой домен в Resend)
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/jackc/pgx/v5/pgxpool"

	"tropa/internal/auth"
	"tropa/internal/store"
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
	cfg := loadConfig(os.Getenv)
	cmd := "serve"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var err error
	switch cmd {
	case "serve":
		err = serve(ctx, log, cfg)
	case "migrate":
		err = withDB(ctx, cfg.DatabaseURL, func(pool *pgxpool.Pool) error { return migrate(ctx, log, pool) })
	case "seed":
		err = seed(ctx, log, cfg.DatabaseURL, os.Args[2:])
	case "osm-fetch":
		err = osmFetch(ctx, log, os.Args[2:])
	case "make-admin":
		if len(os.Args) != 3 {
			err = errors.New("использование: tropa make-admin <email>")
			break
		}
		err = withDB(ctx, cfg.DatabaseURL, func(pool *pgxpool.Pool) error {
			if err := migrate(ctx, log, pool); err != nil {
				return err
			}
			if err := auth.New(pool).SetRole(ctx, os.Args[2], "admin"); err != nil {
				return err
			}
			log.Info("роль админа выдана", "email", os.Args[2])
			return nil
		})
	case "reset-link":
		if len(os.Args) != 3 {
			err = errors.New("использование: tropa reset-link <email>")
			break
		}
		err = withDB(ctx, cfg.DatabaseURL, func(pool *pgxpool.Pool) error { return resetLink(ctx, log, pool, cfg.SiteURL, os.Args[2]) })
	default:
		err = fmt.Errorf("неизвестная команда %q (есть: serve, migrate, seed, make-admin, reset-link, osm-fetch)", cmd)
	}
	if err != nil {
		log.Error(err.Error())
		os.Exit(1)
	}
}

func withDB(ctx context.Context, url string, fn func(*pgxpool.Pool) error) error {
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

func migrate(ctx context.Context, log *slog.Logger, pool *pgxpool.Pool) error {
	applied, err := store.Migrate(ctx, pool)
	for _, name := range applied {
		log.Info("миграция применена", "name", name)
	}
	return err
}
