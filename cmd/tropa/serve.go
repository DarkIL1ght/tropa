package main

import (
	"context"
	"log/slog"
	"net/http"
	"time"
	"tropa/internal/auth"
	"tropa/internal/dm"
	"tropa/internal/friends"
	"tropa/internal/mail"
	"tropa/internal/meetups"
	"tropa/internal/notify"
	"tropa/internal/places"
	"tropa/internal/weather"
	"tropa/internal/web"

	"github.com/jackc/pgx/v5/pgxpool"
)

func serve(ctx context.Context, log *slog.Logger, cfg config) error {
	return withDB(ctx, cfg.DatabaseURL, func(pool *pgxpool.Pool) error {
		if err := migrate(ctx, log, pool); err != nil {
			return err
		}
		addr := cfg.Addr
		siteURL := cfg.SiteURL
		if siteURL == "" {
			siteURL = "http://" + addr
		}

		authSvc := auth.New(pool)
		meetupSvc := meetups.New(pool)
		meetupSvc.SiteURL = siteURL
		friendSvc := friends.New(pool)
		friendSvc.SiteURL = siteURL
		dmSvc := dm.New(pool)
		dmSvc.SiteURL = siteURL
		placeSvc := places.New(pool)
		placeSvc.SiteURL = siteURL
		notifySvc := telegram(ctx, log, pool, cfg.TelegramToken, cfg.TelegramAPI)
		requireEmail := cfg.RequireEmail
		friendSvc.OnlyVerified = requireEmail
		var sender mail.Sender
		if cfg.ResendKey != "" {
			sender = mail.NewResend(cfg.ResendKey, cfg.MailFrom)
			log.Info("письма через Resend", "from", cfg.MailFrom, "подтверждение почты обязательно", requireEmail)
		} else {
			log.Info("письма только в лог: нет RESEND_API_KEY", "подтверждение почты обязательно", requireEmail)
		}
		srv, err := web.New(web.Config{Secure: cfg.SecureCookies, TrustProxy: cfg.TrustProxy, SiteURL: siteURL,
			RequireVerification: requireEmail},
			web.Services{Auth: authSvc, Meetups: meetupSvc, Notify: notifySvc, Places: placeSvc, Friends: friendSvc, DM: dmSvc,
				Mail: sender, Weather: almatyWeather(cfg.WeatherURL)}, log)
		if err != nil {
			return err
		}

		go purgeSessions(ctx, log, authSvc, requireEmail)
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
		// открытые потоки чата (SSE) закрываем сами, иначе Shutdown ждёт их до таймаута
		hs.RegisterOnShutdown(srv.Shutdown)
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

// telegram — сервис уведомлений. Без токена или если бот не отвечает на
// getMe (неверный токен, нет сети) — уведомления выключены, сайт работает.
func telegram(ctx context.Context, log *slog.Logger, pool *pgxpool.Pool, token, apiURL string) *notify.Service {
	if token == "" {
		log.Info("уведомления в Telegram выключены: нет TELEGRAM_BOT_TOKEN")
		return nil
	}
	bot := notify.NewBot(token)
	if apiURL != "" {
		bot.BaseURL = apiURL // свой Bot API сервер или подделка для проверки
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
