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
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"tropa/internal/auth"
	"tropa/internal/meetups"
	"tropa/internal/notify"
	"tropa/internal/osm"
	"tropa/internal/places"
	"tropa/internal/store"
	"tropa/internal/weather"
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
	case "osm-fetch":
		err = osmFetch(ctx, log, os.Args[2:])
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
	case "reset-link":
		if len(os.Args) != 3 {
			err = errors.New("использование: tropa reset-link <email>")
			break
		}
		err = withDB(ctx, func(pool *pgxpool.Pool) error { return resetLink(ctx, log, pool, os.Args[2]) })
	default:
		err = fmt.Errorf("неизвестная команда %q (есть: serve, migrate, seed, make-admin, reset-link, osm-fetch)", cmd)
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

// resetLinkTTL — ссылка от админа живёт дольше, чем от бота: её передают вручную.
const resetLinkTTL = 24 * time.Hour

// resetLink печатает ссылку сброса пароля — админ передаёт её пользователю
// без Telegram (сам пароль админ не узнаёт).
func resetLink(ctx context.Context, log *slog.Logger, pool *pgxpool.Pool, email string) error {
	if err := migrate(ctx, log, pool); err != nil {
		return err
	}
	site := strings.TrimRight(os.Getenv("SITE_URL"), "/")
	if site == "" {
		return errors.New("не задан SITE_URL: ссылка должна вести на сайт")
	}
	found, err := auth.New(pool).RequestReset(ctx, email, resetLinkTTL, func(_ pgx.Tx, _ int64, token string) error {
		fmt.Println(site + "/password/reset/" + token)
		return nil
	})
	if err == nil && !found {
		err = fmt.Errorf("пользователь %s не найден", email)
	}
	return err
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

// osmFetch скачивает объекты OSM по области и пишет кандидатов, которых
// ещё нет в data/places. Файл кандидатов — для ручного разбора, seed его
// пропускает.
func osmFetch(ctx context.Context, log *slog.Logger, args []string) error {
	wd, err := os.Getwd()
	if err != nil {
		return err
	}
	dir := filepath.Join(moduleRoot(wd), "data", "places")
	existing, err := places.LoadSeed(os.DirFS(dir))
	if err != nil {
		return fmt.Errorf("%s: %w", dir, err)
	}

	cache := filepath.Join(os.TempDir(), "tropa-osm-raw.json")
	var elems []osm.Element
	switch {
	case len(args) == 2 && args[0] == "--from":
		data, err := os.ReadFile(args[1])
		if err != nil {
			return err
		}
		if err := json.Unmarshal(data, &elems); err != nil {
			return fmt.Errorf("%s: %w", args[1], err)
		}
		log.Info("объекты из файла", "path", args[1], "count", len(elems))
	case len(args) == 0:
		log.Info("запрос к Overpass — до нескольких минут")
		c := &osm.Client{Retries: 3}
		if elems, err = c.Fetch(ctx, osm.Query(places.RegionBBox)); err != nil {
			return err
		}
		raw, _ := json.Marshal(elems)
		if err := os.WriteFile(cache, raw, 0o644); err != nil {
			return err
		}
		log.Info("объекты OSM получены", "count", len(elems), "cache", cache)
	default:
		return errors.New("использование: tropa osm-fetch [--from raw.json]")
	}

	cands := osm.Candidates(elems, osm.NewExisting(existing))
	out := filepath.Join(dir, "candidates.json")
	if err := writePlacesJSON(out, cands); err != nil {
		return err
	}
	byKind := map[string]int{}
	for _, p := range cands {
		byKind[string(p.Category)+"/"+string(p.Kind)]++
	}
	log.Info("кандидаты записаны", "path", out, "count", len(cands), "by_kind", fmt.Sprint(byKind))
	return nil
}

// writePlacesJSON — JSON с отступами, кириллица без экранирования.
func writePlacesJSON(path string, list []*places.Place) error {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if list == nil {
		list = []*places.Place{}
	}
	if err := enc.Encode(list); err != nil {
		return err
	}
	return os.WriteFile(path, buf.Bytes(), 0o644)
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
		addr := os.Getenv("ADDR")
		if addr == "" {
			addr = "127.0.0.1:8080"
		}
		siteURL := strings.TrimRight(os.Getenv("SITE_URL"), "/")
		if siteURL == "" {
			siteURL = "http://" + addr
		}

		authSvc := auth.New(pool)
		meetupSvc := meetups.New(pool)
		meetupSvc.SiteURL = siteURL
		notifySvc := telegram(ctx, log, pool, os.Getenv("TELEGRAM_BOT_TOKEN"))
		srv, err := web.New(web.Config{Secure: os.Getenv("SECURE_COOKIES") == "1", TrustProxy: os.Getenv("TRUST_PROXY") == "1", SiteURL: siteURL},
			web.Services{Auth: authSvc, Meetups: meetupSvc, Notify: notifySvc, Places: places.New(pool),
				Weather: almatyWeather(os.Getenv("WEATHER_URL"))}, log)
		if err != nil {
			return err
		}

		go purgeSessions(ctx, log, authSvc)
		go meetupJobs(ctx, log, meetupSvc)
		if notifySvc.Enabled() {
			go notifySvc.Poll(ctx)
			go notifySvc.RunDispatch(ctx, 10*time.Second)
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

// meetupJobs раз в минуту снимает просроченные предложения мест (места
// уходят дальше по очереди) и ставит напоминания в очередь уведомлений.
func meetupJobs(ctx context.Context, log *slog.Logger, m *meetups.Service) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		if n, err := m.ExpireOffers(ctx); err != nil && ctx.Err() == nil {
			log.Warn("предложения мест", "err", err)
		} else if n > 0 {
			log.Info("сняты просроченные предложения мест", "count", n)
		}
		if err := m.Remind(ctx); err != nil && ctx.Err() == nil {
			log.Warn("напоминания о встречах", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// telegram — сервис уведомлений. Без токена или если бот не отвечает на
// getMe (неверный токен, нет сети) — уведомления выключены, сайт работает.
func telegram(ctx context.Context, log *slog.Logger, pool *pgxpool.Pool, token string) *notify.Service {
	if token == "" {
		log.Info("уведомления в Telegram выключены: нет TELEGRAM_BOT_TOKEN")
		return nil
	}
	bot := notify.NewBot(token)
	if u := os.Getenv("TELEGRAM_API_URL"); u != "" {
		bot.BaseURL = strings.TrimRight(u, "/") // свой Bot API сервер или подделка для проверки
	}
	svc := notify.New(pool, bot, log)
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	name, err := bot.Username(ctx)
	if err != nil {
		log.Error("бот уведомлений недоступен, уведомления выключены", "err", err)
		return nil
	}
	svc.BotName = name
	log.Info("уведомления в Telegram включены", "bot", "@"+name)
	return svc
}

// almatyWeather — погода для центра рамки города (Open-Meteo считает по сетке,
// точная точка не нужна). baseURL "off" — без погоды, пусто — Open-Meteo.
func almatyWeather(baseURL string) *weather.Client {
	if baseURL == "off" {
		return nil
	}
	b := places.CityBBox
	c := weather.New((b.South+b.North)/2, (b.West+b.East)/2)
	if baseURL != "" {
		c.BaseURL = baseURL
	}
	return c
}
