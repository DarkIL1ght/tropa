package main

import (
	"context"
	"log/slog"
	"time"
	"tropa/internal/auth"
	"tropa/internal/meetups"
)

// purgeSessions раз в час удаляет просроченные сессии, а при обязательном
// подтверждении почты — и аккаунты, не подтвердившие её за неделю.
func purgeSessions(ctx context.Context, log *slog.Logger, a *auth.Service, purgeUnverified bool) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		if n, err := a.PurgeExpired(ctx); err != nil && ctx.Err() == nil {
			log.Warn("очистка сессий", "err", err)
		} else if n > 0 {
			log.Info("удалены просроченные сессии", "count", n)
		}
		// без обязательного подтверждения неподтверждённые — обычные пользователи, их не трогаем
		if purgeUnverified {
			if n, err := a.PurgeUnverified(ctx); err != nil && ctx.Err() == nil {
				log.Warn("очистка неподтверждённых аккаунтов", "err", err)
			} else if n > 0 {
				log.Info("удалены аккаунты без подтверждённой почты", "count", n)
			}
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
