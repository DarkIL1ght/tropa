// Package dm — личные сообщения. Писать друг другу могут только друзья:
// дружба взаимная, поэтому незнакомец не может написать первым.
package dm

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"tropa/internal/auth"
	"tropa/internal/notify"
)

const (
	// MaxMessage — предел длины сообщения в символах (как CHECK в миграции 012).
	MaxMessage = 1000
	// HistorySize — сколько последних сообщений показывать при открытии переписки.
	HistorySize = 100
	// pingEvery — не чаще одного уведомления в Telegram на переписку за этот промежуток.
	pingEvery = 15 * time.Minute
)

var (
	ErrNotFriends = errors.New("писать можно только друзьям")
	ErrBadMessage = fmt.Errorf("сообщение — от 1 до %d символов", MaxMessage)
)

type Service struct {
	DB  *pgxpool.Pool
	Now func() time.Time
	// SiteURL — адрес сайта без / в конце, для ссылок в уведомлениях.
	SiteURL string
}

func New(db *pgxpool.Pool) *Service { return &Service{DB: db, Now: time.Now} }

type Message struct {
	ID        int64
	FromID    int64
	ToID      int64
	Name      string // имя отправителя
	Body      string
	CreatedAt time.Time
}

// Key — общий ключ переписки пары в любом порядке (для потоков SSE).
func Key(a, b int64) string { return fmt.Sprintf("dm:%d:%d", min(a, b), max(a, b)) }

// Thread — друг в списке переписок: последнее сообщение и сколько непрочитанных.
type Thread struct {
	UserID int64
	Name   string
	Last   *Message // nil — переписки ещё нет
	Unread int
}

// Post сохраняет сообщение и ставит получателю уведомление в Telegram (не
// чаще раза в pingEvery). Дружба проверяется в той же транзакции под
// FOR SHARE: удалённый из друзей одновременно с отправкой написать не успеет.
func (s *Service) Post(ctx context.Context, from *auth.User, to int64, body string) (Message, error) {
	body = strings.TrimSpace(body)
	if body == "" || utf8.RuneCountInString(body) > MaxMessage {
		return Message{}, ErrBadMessage
	}
	msg := Message{FromID: from.ID, ToID: to, Name: from.Name, Body: body}
	err := pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		var ok bool
		err := tx.QueryRow(ctx, `SELECT true FROM friendships
			WHERE status = 'accepted' AND ((user_id = $1 AND friend_id = $2) OR (user_id = $2 AND friend_id = $1))
			FOR SHARE`, from.ID, to).Scan(&ok)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFriends
		}
		if err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO direct_messages (sender_id, recipient_id, body) VALUES ($1, $2, $3)
			RETURNING id, created_at`, from.ID, to, body).Scan(&msg.ID, &msg.CreatedAt); err != nil {
			return err
		}
		bucket := s.Now().Unix() / int64(pingEvery/time.Second)
		key := fmt.Sprintf("dm:%d:%d:%d", from.ID, to, bucket)
		return notify.Enqueue(ctx, tx, to, "dm", s.text(msg), key)
	})
	return msg, err
}

func (s *Service) text(m Message) string {
	body := m.Body
	if r := []rune(body); len(r) > 200 {
		body = string(r[:200]) + "…"
	}
	return fmt.Sprintf("Новое сообщение от %s\n%s\n\nОтветить: %s/messages/%d", m.Name, body, s.SiteURL, m.FromID)
}

// Conversation — переписка me с other по порядку. afterID = 0 — последние
// HistorySize; иначе — все после afterID (догрузка потока).
func (s *Service) Conversation(ctx context.Context, me, other, afterID int64) ([]Message, error) {
	limit := HistorySize
	if afterID > 0 {
		limit = 1000 // после обрыва связи догружаем всё пропущенное
	}
	rows, err := s.DB.Query(ctx, `SELECT * FROM (
		SELECT d.id, d.sender_id, d.recipient_id, u.name, d.body, d.created_at
		  FROM direct_messages d JOIN users u ON u.id = d.sender_id
		 WHERE least(d.sender_id, d.recipient_id) = least($1::bigint, $2::bigint)
		   AND greatest(d.sender_id, d.recipient_id) = greatest($1::bigint, $2::bigint)
		   AND d.id > $3
		 ORDER BY d.id DESC LIMIT $4) last ORDER BY id`, me, other, afterID, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, scanMessage)
}

func scanMessage(row pgx.CollectableRow) (Message, error) {
	var m Message
	err := row.Scan(&m.ID, &m.FromID, &m.ToID, &m.Name, &m.Body, &m.CreatedAt)
	return m, err
}

// MarkRead отмечает прочитанными сообщения от other к me.
func (s *Service) MarkRead(ctx context.Context, me, other int64) error {
	_, err := s.DB.Exec(ctx, `UPDATE direct_messages SET read_at = $3
		WHERE recipient_id = $1 AND sender_id = $2 AND read_at IS NULL`, me, other, s.Now())
	return err
}

// Unread — сколько всего непрочитанных сообщений у me (счётчик в шапке).
func (s *Service) Unread(ctx context.Context, me int64) (int, error) {
	var n int
	err := s.DB.QueryRow(ctx, `SELECT count(*) FROM direct_messages WHERE recipient_id = $1 AND read_at IS NULL`, me).Scan(&n)
	return n, err
}

// Threads — все друзья me: сначала те, с кем недавно переписывались, потом
// остальные по имени. Писать можно любому другу, даже без истории.
func (s *Service) Threads(ctx context.Context, me int64) ([]Thread, error) {
	rows, err := s.DB.Query(ctx, `
		WITH friends AS (
			SELECT CASE WHEN f.user_id = $1 THEN f.friend_id ELSE f.user_id END AS id
			  FROM friendships f
			 WHERE f.status = 'accepted' AND (f.user_id = $1 OR f.friend_id = $1)
		)
		SELECT u.id, u.name,
		       l.id, l.sender_id, l.recipient_id, l.name, l.body, l.created_at,
		       (SELECT count(*) FROM direct_messages x
		         WHERE x.recipient_id = $1 AND x.sender_id = u.id AND x.read_at IS NULL)
		  FROM friends fr JOIN users u ON u.id = fr.id
		  LEFT JOIN LATERAL (
			SELECT d.id, d.sender_id, d.recipient_id, su.name, d.body, d.created_at
			  FROM direct_messages d JOIN users su ON su.id = d.sender_id
			 WHERE least(d.sender_id, d.recipient_id) = least($1::bigint, u.id)
			   AND greatest(d.sender_id, d.recipient_id) = greatest($1::bigint, u.id)
			 ORDER BY d.id DESC LIMIT 1) l ON true
		 ORDER BY l.id DESC NULLS LAST, u.name`, me)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Thread, error) {
		var t Thread
		var id, from, to *int64
		var name, body *string
		var at *time.Time
		if err := row.Scan(&t.UserID, &t.Name, &id, &from, &to, &name, &body, &at, &t.Unread); err != nil {
			return t, err
		}
		if id != nil {
			t.Last = &Message{ID: *id, FromID: *from, ToID: *to, Name: *name, Body: *body, CreatedAt: *at}
		}
		return t, nil
	})
}
