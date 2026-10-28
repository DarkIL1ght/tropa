package auth

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// AdminUser — строка списка пользователей в админке.
type AdminUser struct {
	User
	Telegram  bool       // подключены уведомления
	Organized int        // собрал встреч (не отменённых)
	LastSeen  *time.Time // последний вход (создание сессии); nil — входов не было или сессии истекли
}

// AdminLimit — сколько пользователей показывает админка за раз.
const AdminLimit = 200

// ListUsers — пользователи для админки, новые сверху; q ищет по email и имени.
func (s *Service) ListUsers(ctx context.Context, q string) ([]AdminUser, error) {
	q = strings.TrimSpace(q)
	rows, err := s.DB.Query(ctx, `
		SELECT u.id, u.email, u.name, u.role, u.created_at,
		       u.telegram_chat_id IS NOT NULL,
		       (SELECT count(*) FROM meetups m WHERE m.organizer_id = u.id AND m.status <> 'cancelled'),
		       (SELECT max(created_at) FROM sessions x WHERE x.user_id = u.id)
		  FROM users u
		 WHERE $1 = '' OR u.email ILIKE '%' || $1 || '%' OR u.name ILIKE '%' || $1 || '%'
		 ORDER BY u.id DESC
		 LIMIT $2`, likeEscape(q), AdminLimit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (AdminUser, error) {
		var a AdminUser
		err := row.Scan(&a.ID, &a.Email, &a.Name, &a.Role, &a.CreatedAt, &a.Telegram, &a.Organized, &a.LastSeen)
		return a, err
	})
}

// likeEscape экранирует % и _ — поиск ищет их буквально.
func likeEscape(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// AdminStats — счётчики для шапки админки.
type AdminStats struct {
	Users, Telegram, NewWeek int
}

func (s *Service) AdminStats(ctx context.Context) (AdminStats, error) {
	var st AdminStats
	err := s.DB.QueryRow(ctx, `SELECT count(*), count(telegram_chat_id), count(*) FILTER (WHERE created_at > $1) FROM users`,
		s.Now().Add(-7*24*time.Hour)).Scan(&st.Users, &st.Telegram, &st.NewWeek)
	return st, err
}

// ErrLastAdmin — нельзя снять роль с последнего админа: в админку станет некому зайти.
var ErrLastAdmin = errors.New("это последний админ — сначала назначьте другого")

// SetRoleByID меняет роль пользователя из админки. Снять роль с последнего
// админа нельзя; проверка и запись — в одной транзакции под блокировкой админов.
func (s *Service) SetRoleByID(ctx context.Context, id int64, role string) error {
	if role != "user" && role != "admin" {
		return ErrInvalidInput
	}
	return pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		if role == "user" {
			var others int
			// FOR UPDATE: два админа не снимут друг друга одновременно
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM (SELECT id FROM users WHERE role = 'admin' AND id <> $1 FOR UPDATE) a`, id).Scan(&others); err != nil {
				return err
			}
			if others == 0 {
				return ErrLastAdmin
			}
		}
		tag, err := tx.Exec(ctx, `UPDATE users SET role = $2 WHERE id = $1`, id, role)
		if err == nil && tag.RowsAffected() == 0 {
			err = ErrNoUser
		}
		return err
	})
}
