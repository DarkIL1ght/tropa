// Package friends — взаимная дружба через заявку: один отправляет, второй
// принимает. Встречная заявка (оба нажали «Добавить») сразу делает друзьями.
package friends

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"tropa/internal/auth"
	"tropa/internal/notify"
)

// Status — отношение текущего пользователя к другому.
type Status string

const (
	None     Status = ""         // никак
	Outgoing Status = "outgoing" // я отправил заявку
	Incoming Status = "incoming" // мне отправили заявку
	Friends  Status = "friends"
)

var (
	ErrSelf     = errors.New("нельзя добавить в друзья самого себя")
	ErrNoUser   = errors.New("пользователь не найден")
	ErrNoInvite = errors.New("заявки нет — возможно, её уже отменили")
)

type Service struct {
	DB  *pgxpool.Pool
	Now func() time.Time
	// SiteURL — адрес сайта без / в конце, для ссылок в уведомлениях.
	SiteURL string
}

func New(db *pgxpool.Pool) *Service { return &Service{DB: db, Now: time.Now} }

// Person — друг или автор заявки в списках.
type Person struct {
	ID    int64
	Name  string
	Since time.Time // дружба принята или заявка отправлена
}

// Lists — всё для страницы «Друзья».
type Lists struct {
	Friends, Incoming, Outgoing []Person
}

// pairLock блокирует пару (в любом порядке) до конца транзакции: две
// встречные заявки или заявка и отмена не должны разойтись.
func pairLock(ctx context.Context, tx pgx.Tx, a, b int64) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('friends:' || least($1::bigint, $2::bigint) || ':' || greatest($1::bigint, $2::bigint), 0))`, a, b)
	return err
}

func status(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, me, other int64) (Status, error) {
	var from int64
	var st string
	err := q.QueryRow(ctx, `SELECT user_id, status FROM friendships
		WHERE (user_id = $1 AND friend_id = $2) OR (user_id = $2 AND friend_id = $1)`, me, other).Scan(&from, &st)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return None, nil
	case err != nil:
		return None, err
	case st == "accepted":
		return Friends, nil
	case from == me:
		return Outgoing, nil
	default:
		return Incoming, nil
	}
}

// Status — отношение me к other.
func (s *Service) Status(ctx context.Context, me, other int64) (Status, error) {
	return status(ctx, s.DB, me, other)
}

// Request отправляет заявку. Если other уже прислал заявку me — это
// согласие: сразу друзья. Повторная заявка ничего не меняет.
func (s *Service) Request(ctx context.Context, me *auth.User, other int64) (Status, error) {
	if me.ID == other {
		return None, ErrSelf
	}
	var result Status
	err := pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		if err := pairLock(ctx, tx, me.ID, other); err != nil {
			return err
		}
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM users WHERE id = $1)`, other).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return ErrNoUser
		}
		st, err := status(ctx, tx, me.ID, other)
		if err != nil {
			return err
		}
		switch st {
		case Incoming:
			result = Friends
			return s.accept(ctx, tx, me, other)
		case Outgoing, Friends:
			result = st
			return nil
		}
		result = Outgoing
		if _, err := tx.Exec(ctx, `INSERT INTO friendships (user_id, friend_id, created_at) VALUES ($1, $2, $3)`, me.ID, other, s.Now()); err != nil {
			return err
		}
		return notify.Enqueue(ctx, tx, other, "friend_request",
			fmt.Sprintf("%s хочет добавить вас в друзья в Tropa.\nПринять или отклонить: %s/friends", me.Name, s.SiteURL), "")
	})
	return result, err
}

// Accept принимает заявку от from.
func (s *Service) Accept(ctx context.Context, me *auth.User, from int64) error {
	return pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		if err := pairLock(ctx, tx, me.ID, from); err != nil {
			return err
		}
		st, err := status(ctx, tx, me.ID, from)
		if err != nil {
			return err
		}
		if st == Friends {
			return nil
		}
		if st != Incoming {
			return ErrNoInvite
		}
		return s.accept(ctx, tx, me, from)
	})
}

func (s *Service) accept(ctx context.Context, tx pgx.Tx, me *auth.User, from int64) error {
	if _, err := tx.Exec(ctx, `UPDATE friendships SET status = 'accepted', accepted_at = $3
		WHERE user_id = $1 AND friend_id = $2`, from, me.ID, s.Now()); err != nil {
		return err
	}
	return notify.Enqueue(ctx, tx, from, "friend_accepted",
		fmt.Sprintf("%s теперь у вас в друзьях в Tropa. Встречи «для друзей» друг друга вы теперь видите в разделе «Друзья»: %s/friends", me.Name, s.SiteURL), "")
}

// Remove разрывает связь с other в любом состоянии: удаляет из друзей,
// отклоняет входящую или отменяет свою заявку. Без уведомлений.
func (s *Service) Remove(ctx context.Context, me, other int64) error {
	_, err := s.DB.Exec(ctx, `DELETE FROM friendships
		WHERE (user_id = $1 AND friend_id = $2) OR (user_id = $2 AND friend_id = $1)`, me, other)
	return err
}

// List — друзья и заявки пользователя: друзья по имени, заявки — новые сверху.
func (s *Service) List(ctx context.Context, me int64) (Lists, error) {
	rows, err := s.DB.Query(ctx, `
		SELECT CASE WHEN f.user_id = $1 THEN f.friend_id ELSE f.user_id END, u.name,
		       coalesce(f.accepted_at, f.created_at),
		       CASE WHEN f.status = 'accepted' THEN 'friends' WHEN f.user_id = $1 THEN 'outgoing' ELSE 'incoming' END
		  FROM friendships f
		  JOIN users u ON u.id = CASE WHEN f.user_id = $1 THEN f.friend_id ELSE f.user_id END
		 WHERE f.user_id = $1 OR f.friend_id = $1
		 ORDER BY f.status = 'accepted', lower(u.name), f.created_at DESC`, me)
	if err != nil {
		return Lists{}, err
	}
	defer rows.Close()
	var l Lists
	for rows.Next() {
		var p Person
		var st Status
		if err := rows.Scan(&p.ID, &p.Name, &p.Since, &st); err != nil {
			return Lists{}, err
		}
		switch st {
		case Friends:
			l.Friends = append(l.Friends, p)
		case Incoming:
			l.Incoming = append(l.Incoming, p)
		default:
			l.Outgoing = append(l.Outgoing, p)
		}
	}
	return l, rows.Err()
}

// IDs — множество id друзей (отметить «друг» в списке участников).
func (s *Service) IDs(ctx context.Context, me int64) (map[int64]bool, error) {
	rows, err := s.DB.Query(ctx, `SELECT CASE WHEN user_id = $1 THEN friend_id ELSE user_id END
		FROM friendships WHERE (user_id = $1 OR friend_id = $1) AND status = 'accepted'`, me)
	if err != nil {
		return nil, err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	out := make(map[int64]bool, len(ids))
	for _, id := range ids {
		out[id] = true
	}
	return out, err
}

// Counts — число друзей и входящих заявок (для профиля и шапки).
func (s *Service) Counts(ctx context.Context, me int64) (friends, incoming int, err error) {
	err = s.DB.QueryRow(ctx, `SELECT
		count(*) FILTER (WHERE status = 'accepted'),
		count(*) FILTER (WHERE status = 'pending' AND friend_id = $1)
		FROM friendships WHERE user_id = $1 OR friend_id = $1`, me).Scan(&friends, &incoming)
	return
}
