// Package mail — отправка писем. Пока настоящего почтового сервиса нет,
// письма пишутся в лог сервера (LogSender); SMTP или API добавятся вторым Sender.
package mail

import (
	"context"
	"log/slog"
)

// Sender отправляет одно письмо.
type Sender interface {
	Send(ctx context.Context, to, subject, body string) error
}

// LogSender вместо отправки пишет письмо в лог: для разработки и пока
// почта не подключена. Код подтверждения виден в логе сервера.
type LogSender struct{ Log *slog.Logger }

func (l LogSender) Send(ctx context.Context, to, subject, body string) error {
	l.Log.Info("письмо (почта не подключена, только в лог)", "to", to, "subject", subject, "body", body)
	return nil
}
