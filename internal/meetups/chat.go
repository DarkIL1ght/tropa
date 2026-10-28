package meetups

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"tropa/internal/auth"
	"tropa/internal/notify"
)

const (
	// MaxMessage — предел длины сообщения в символах (как CHECK в миграции 008).
	MaxMessage = 1000
	// HistorySize — сколько последних сообщений показывать при открытии встречи.
	HistorySize = 100
	// chatPingEvery — не чаще одного уведомления о чате на человека и встречу
	// за этот промежуток: переписка не должна заваливать Telegram.
	chatPingEvery = 15 * time.Minute
)

var (
	ErrNoChat     = errors.New("чат доступен только участникам встречи")
	ErrBadMessage = fmt.Errorf("сообщение — от 1 до %d символов", MaxMessage)
)

type Message struct {
	ID        int64
	MeetupID  int64
	UserID    int64
	Name      string
	Body      string
	CreatedAt time.Time
}

// CanChat — может ли участник читать и писать в чат: идёт или ему
// предложено место. Очередь чат не видит — она ещё не в компании.
func CanChat(p *Participant) bool { return p != nil && p.Status != Waitlist }

// Messages — сообщения встречи по порядку. afterID = 0 — последние
// HistorySize; иначе — все после afterID (догрузка потока).
func (s *Service) Messages(ctx context.Context, meetupID, afterID int64) ([]Message, error) {
	q := `SELECT * FROM (
		SELECT x.id, x.meetup_id, x.user_id, u.name, x.body, x.created_at
		  FROM messages x JOIN users u ON u.id = x.user_id
		 WHERE x.meetup_id = $1 AND x.id > $2
		 ORDER BY x.id DESC LIMIT $3) last ORDER BY id`
	limit := HistorySize
	if afterID > 0 {
		limit = 1000 // после обрыва связи догружаем всё пропущенное
	}
	rows, err := s.DB.Query(ctx, q, meetupID, afterID, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Message, error) {
		var m Message
		err := row.Scan(&m.ID, &m.MeetupID, &m.UserID, &m.Name, &m.Body, &m.CreatedAt)
		return m, err
	})
}

// Post сохраняет сообщение участника и ставит остальным участникам
// уведомление в Telegram (не чаще раза в chatPingEvery). Право писать
// проверяется в той же транзакции, под блокировкой строки участника:
// вышедший из встречи одновременно с отправкой написать не успеет.
func (s *Service) Post(ctx context.Context, m *Meetup, u *auth.User, body string) (Message, error) {
	body = strings.TrimSpace(body)
	if body == "" || utf8.RuneCountInString(body) > MaxMessage {
		return Message{}, ErrBadMessage
	}
	msg := Message{MeetupID: m.ID, UserID: u.ID, Name: u.Name, Body: body}
	err := pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		var st PStatus
		err := tx.QueryRow(ctx, `SELECT status FROM participants WHERE meetup_id = $1 AND user_id = $2 FOR SHARE`, m.ID, u.ID).Scan(&st)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && st == Waitlist) {
			return ErrNoChat
		}
		if err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO messages (meetup_id, user_id, body) VALUES ($1, $2, $3) RETURNING id, created_at`,
			m.ID, u.ID, body).Scan(&msg.ID, &msg.CreatedAt); err != nil {
			return err
		}
		return s.notifyChat(ctx, tx, m, msg)
	})
	return msg, err
}

func (s *Service) notifyChat(ctx context.Context, tx pgx.Tx, m *Meetup, msg Message) error {
	rows, err := tx.Query(ctx, `SELECT user_id FROM participants WHERE meetup_id = $1 AND user_id <> $2 AND status <> 'waitlist'`, m.ID, msg.UserID)
	if err != nil {
		return err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return err
	}
	bucket := s.Now().Unix() / int64(chatPingEvery/time.Second)
	text := s.chatText(m, msg)
	for _, id := range ids {
		key := fmt.Sprintf("chat:%d:%d:%d", m.ID, id, bucket)
		if err := notify.Enqueue(ctx, tx, id, "chat", text, key); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) chatText(m *Meetup, msg Message) string {
	body := msg.Body
	if r := []rune(body); len(r) > 200 {
		body = string(r[:200]) + "…"
	}
	return fmt.Sprintf("Новое в чате встречи «%s»\n%s: %s\n\nОтветить: %s#chat", m.Title, msg.Name, body, s.link(m))
}
