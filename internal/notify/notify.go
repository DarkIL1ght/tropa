// Package notify — уведомления в Telegram: привязка аккаунта к чату с ботом,
// очередь отправки (outbox) и воркеры отправки и приёма сообщений.
//
// Событие и уведомление пишутся в одной транзакции (Enqueue принимает tx),
// а отправляет их отдельный воркер (Dispatch) с повторами. Поэтому падение
// Telegram или сервера между событием и отправкой уведомление не теряет.
package notify

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	LinkTTL     = 15 * time.Minute
	MaxAttempts = 8
	batchSize   = 20
	// lease — на сколько воркер «забирает» уведомление перед отправкой:
	// если он упадёт посреди отправки, через это время уведомление возьмут снова.
	lease = 2 * time.Minute
)

// Execer — пул или транзакция.
type Execer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// Enqueue ставит уведомление в очередь — только если у пользователя
// подключён Telegram. dedupKey (если не пуст) не даёт поставить то же
// уведомление дважды. Вызывать в транзакции события.
func Enqueue(ctx context.Context, q Execer, userID int64, kind, text, dedupKey string) error {
	var key *string
	if dedupKey != "" {
		key = &dedupKey
	}
	_, err := q.Exec(ctx,
		`INSERT INTO notifications (user_id, kind, text, dedup_key)
		 SELECT id, $2, $3, $4 FROM users WHERE id = $1 AND telegram_chat_id IS NOT NULL
		 ON CONFLICT (dedup_key) DO NOTHING`, userID, kind, text, key)
	return err
}

type Service struct {
	DB  *pgxpool.Pool
	Bot *Bot
	Log *slog.Logger
	Now func() time.Time
	// BotName — имя бота для ссылок t.me/<имя>; берётся из getMe при старте.
	BotName string
}

func New(db *pgxpool.Pool, bot *Bot, log *slog.Logger) *Service {
	return &Service{DB: db, Bot: bot, Log: log, Now: time.Now}
}

// Enabled — настроен ли бот. Без него сайт работает, но уведомлений нет.
func (s *Service) Enabled() bool { return s != nil && s.Bot != nil && s.BotName != "" }

func hashCode(code string) []byte {
	h := sha256.Sum256([]byte(code))
	return h[:]
}

// LinkURL создаёт одноразовый код привязки и возвращает ссылку на бота.
// Старые коды пользователя удаляются: действует только последний.
func (s *Service) LinkURL(ctx context.Context, userID int64) (string, error) {
	b := make([]byte, 18)
	rand.Read(b)
	code := base64.RawURLEncoding.EncodeToString(b) // 24 символа [A-Za-z0-9_-] — допустимо для /start
	err := pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM telegram_links WHERE user_id = $1 OR expires_at < $2`, userID, s.Now()); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO telegram_links (code_hash, user_id, expires_at) VALUES ($1, $2, $3)`,
			hashCode(code), userID, s.Now().Add(LinkTTL))
		return err
	})
	if err != nil {
		return "", err
	}
	return "https://t.me/" + s.BotName + "?start=" + code, nil
}

var ErrBadLink = errors.New("ссылка для подключения неверна или устарела")

// Link привязывает чат по коду из /start. Код одноразовый. Если этот чат
// был привязан к другому аккаунту, привязка переезжает на новый.
func (s *Service) Link(ctx context.Context, code string, chatID int64) (name string, err error) {
	err = pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		var userID int64
		err := tx.QueryRow(ctx,
			`DELETE FROM telegram_links WHERE code_hash = $1 AND expires_at > $2 RETURNING user_id`,
			hashCode(code), s.Now()).Scan(&userID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrBadLink
		}
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE users SET telegram_chat_id = NULL WHERE telegram_chat_id = $1`, chatID); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `UPDATE users SET telegram_chat_id = $2 WHERE id = $1 RETURNING name`, userID, chatID).Scan(&name)
	})
	return name, err
}

// Linked — подключён ли Telegram у пользователя.
func (s *Service) Linked(ctx context.Context, userID int64) (bool, error) {
	var ok bool
	err := s.DB.QueryRow(ctx, `SELECT telegram_chat_id IS NOT NULL FROM users WHERE id = $1`, userID).Scan(&ok)
	return ok, err
}

// Unlink отключает Telegram у пользователя; неотправленные уведомления отбрасываются.
func (s *Service) Unlink(ctx context.Context, userID int64) error {
	return pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE users SET telegram_chat_id = NULL WHERE id = $1`, userID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx,
			`UPDATE notifications SET failed_at = $2, last_error = 'Telegram отключён'
			  WHERE user_id = $1 AND sent_at IS NULL AND failed_at IS NULL`, userID, s.Now())
		return err
	})
}

func (s *Service) unlinkChat(ctx context.Context, chatID int64) (bool, error) {
	var id int64
	err := s.DB.QueryRow(ctx, `SELECT id FROM users WHERE telegram_chat_id = $1`, chatID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, s.Unlink(ctx, id)
}

type job struct {
	id       int64
	userID   int64
	chatID   int64
	text     string
	attempts int
}

// Dispatch отправляет пачку уведомлений, которым подошло время, и возвращает,
// сколько отправлено. Строки сначала «забираются» (next_attempt_at сдвигается
// на lease) под SKIP LOCKED, поэтому два воркера не отправят одно и то же,
// а HTTP-запросы идут вне транзакции.
func (s *Service) Dispatch(ctx context.Context) (int, error) {
	now := s.Now()
	rows, err := s.DB.Query(ctx,
		`UPDATE notifications n SET next_attempt_at = $2, attempts = n.attempts + 1
		   FROM users u
		  WHERE u.id = n.user_id AND n.id IN (
		        SELECT id FROM notifications
		         WHERE sent_at IS NULL AND failed_at IS NULL AND next_attempt_at <= $1
		         ORDER BY next_attempt_at, id LIMIT $3 FOR UPDATE SKIP LOCKED)
		 RETURNING n.id, n.user_id, coalesce(u.telegram_chat_id, 0), n.text, n.attempts`,
		now, now.Add(lease), batchSize)
	if err != nil {
		return 0, err
	}
	jobs, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (job, error) {
		var j job
		err := r.Scan(&j.id, &j.userID, &j.chatID, &j.text, &j.attempts)
		return j, err
	})
	if err != nil {
		return 0, err
	}
	sent := 0
	for _, j := range jobs {
		if j.chatID == 0 {
			// Telegram отключили, пока уведомление ждало
			err = s.finish(ctx, j.id, "failed_at", "Telegram отключён")
		} else if err = s.Bot.Send(ctx, j.chatID, j.text); err == nil {
			sent++
			err = s.finish(ctx, j.id, "sent_at", "")
		} else {
			err = s.retry(ctx, j, err)
		}
		if err != nil {
			return sent, err
		}
	}
	return sent, nil
}

func (s *Service) finish(ctx context.Context, id int64, column, msg string) error {
	_, err := s.DB.Exec(ctx, `UPDATE notifications SET `+column+` = $2, last_error = $3 WHERE id = $1`, id, s.Now(), msg)
	return err
}

// retry решает, что делать после неудачной отправки: бот заблокирован —
// отключить Telegram; постоянная ошибка или кончились попытки — бросить;
// иначе — повторить позже (Telegram сам говорит, сколько ждать при 429).
func (s *Service) retry(ctx context.Context, j job, sendErr error) error {
	var apiErr *APIError
	isAPI := errors.As(sendErr, &apiErr)
	switch {
	case isAPI && apiErr.Blocked():
		s.Log.Info("бот заблокирован, Telegram отключён", "user", j.userID)
		if err := s.Unlink(ctx, j.userID); err != nil {
			return err
		}
		return s.finish(ctx, j.id, "failed_at", sendErr.Error())
	case isAPI && apiErr.Permanent(), j.attempts >= MaxAttempts:
		s.Log.Warn("уведомление не отправлено", "id", j.id, "err", sendErr)
		return s.finish(ctx, j.id, "failed_at", sendErr.Error())
	}
	wait := Backoff(j.attempts)
	if isAPI && apiErr.RetryAfter > 0 {
		wait = apiErr.RetryAfter
	}
	_, err := s.DB.Exec(ctx, `UPDATE notifications SET next_attempt_at = $2, last_error = $3 WHERE id = $1`,
		j.id, s.Now().Add(wait), sendErr.Error())
	return err
}

// Backoff — пауза перед повтором после attempt-й неудачи: 30 с, 1 мин, 2 мин… до 1 ч.
func Backoff(attempt int) time.Duration {
	d := time.Duration(30*math.Pow(2, float64(attempt-1))) * time.Second
	return min(d, time.Hour)
}

// Тексты ответов бота.
const (
	replyHello = "Привет! Это бот уведомлений Tropa.\n\n" +
		"Чтобы получать уведомления о встречах, откройте профиль на сайте и нажмите «Подключить Telegram» — " +
		"бот откроется по специальной ссылке и сам привяжет аккаунт.\n\n/stop — отключить уведомления."
	replyBadLink = "Ссылка для подключения устарела или уже использована. " +
		"Откройте профиль на сайте и нажмите «Подключить Telegram» ещё раз — ссылка действует 15 минут."
	replyStopped    = "Уведомления отключены. Подключить снова можно в профиле на сайте."
	replyNotLinked  = "Этот чат не привязан к аккаунту Tropa."
	replyOnlyDirect = "Уведомления работают только в личном чате с ботом."
)

func replyLinked(name string) string {
	return fmt.Sprintf("Готово, %s! Аккаунт привязан. Сюда будут приходить уведомления: "+
		"освободилось место во встрече, встречу отменили или перенесли, напоминание за день.\n\n/stop — отключить.", name)
}

// Handle обрабатывает одно входящее сообщение и отвечает на него.
func (s *Service) Handle(ctx context.Context, u Update) error {
	if u.Message == nil {
		return nil
	}
	chat := u.Message.Chat.ID
	cmd, arg, _ := strings.Cut(strings.TrimSpace(u.Message.Text), " ")
	cmd, _, _ = strings.Cut(cmd, "@") // /start@TropaBot в группах
	if u.Message.Chat.Type != "private" {
		if cmd == "/start" {
			return s.Bot.Send(ctx, chat, replyOnlyDirect)
		}
		return nil
	}
	switch cmd {
	case "/start":
		if arg == "" {
			return s.Bot.Send(ctx, chat, replyHello)
		}
		name, err := s.Link(ctx, strings.TrimSpace(arg), chat)
		if errors.Is(err, ErrBadLink) {
			return s.Bot.Send(ctx, chat, replyBadLink)
		}
		if err != nil {
			return err
		}
		return s.Bot.Send(ctx, chat, replyLinked(name))
	case "/stop":
		ok, err := s.unlinkChat(ctx, chat)
		if err != nil {
			return err
		}
		if !ok {
			return s.Bot.Send(ctx, chat, replyNotLinked)
		}
		return s.Bot.Send(ctx, chat, replyStopped)
	}
	return s.Bot.Send(ctx, chat, replyHello)
}

// Poll принимает сообщения боту (long polling), пока не отменён ctx.
func (s *Service) Poll(ctx context.Context) {
	var offset int64
	for ctx.Err() == nil {
		ups, err := s.Bot.Updates(ctx, offset, 25*time.Second)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			s.Log.Warn("telegram getUpdates", "err", err)
			sleep(ctx, 5*time.Second)
			continue
		}
		for _, u := range ups {
			offset = u.ID + 1
			if err := s.Handle(ctx, u); err != nil {
				s.Log.Warn("telegram: сообщение боту", "err", err)
			}
		}
	}
}

// RunDispatch раз в interval отправляет очередь, пока не отменён ctx.
func (s *Service) RunDispatch(ctx context.Context, interval time.Duration) {
	for ctx.Err() == nil {
		n, err := s.Dispatch(ctx)
		if err != nil && ctx.Err() == nil {
			s.Log.Warn("отправка уведомлений", "err", err)
		}
		if n == batchSize {
			continue // очередь длинная — следующую пачку сразу
		}
		sleep(ctx, interval)
	}
}

func sleep(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}
