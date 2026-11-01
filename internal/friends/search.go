package friends

import (
	"context"
	"net/mail"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

// SearchLimit — сколько людей показывает поиск за раз.
const SearchLimit = 20

// Found — человек в результатах поиска и отношение к нему.
type Found struct {
	ID     int64
	Name   string
	Status Status
}

// Search ищет людей, которых можно добавить в друзья: по части имени или по
// email целиком. Email в результатах не показывается и по части не ищется —
// так адреса нельзя перебрать. Себя и аккаунты без подтверждённой почты не
// показывает. Запрос короче 2 символов — пустой результат.
func (s *Service) Search(ctx context.Context, me int64, q string) ([]Found, error) {
	q = strings.TrimSpace(q)
	if utf8.RuneCountInString(q) < 2 {
		return nil, nil
	}
	email := ""
	if a, err := mail.ParseAddress(q); err == nil && a.Address == q {
		email = q
	}
	rows, err := s.DB.Query(ctx, `
		SELECT u.id, u.name,
		       CASE WHEN f.status = 'accepted' THEN 'friends'
		            WHEN f.user_id = $1 THEN 'outgoing'
		            WHEN f.user_id IS NOT NULL THEN 'incoming'
		            ELSE '' END
		  FROM users u
		  LEFT JOIN friendships f ON (f.user_id = $1 AND f.friend_id = u.id) OR (f.user_id = u.id AND f.friend_id = $1)
		 WHERE u.id <> $1 AND u.email_verified_at IS NOT NULL
		   AND (CASE WHEN $3 <> '' THEN lower(u.email) = lower($3)
		             ELSE u.name ILIKE '%' || $2 || '%' END)
		 ORDER BY f.status = 'accepted' DESC NULLS LAST, lower(u.name), u.id
		 LIMIT $4`, me, likeEscape(q), email, SearchLimit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Found, error) {
		var f Found
		err := row.Scan(&f.ID, &f.Name, &f.Status)
		return f, err
	})
}

// likeEscape экранирует % и _ — поиск ищет их буквально.
func likeEscape(s string) string {
	return strings.NewReplacer(`\`, `\`, `%`, `\%`, `_`, `\_`).Replace(s)
}
