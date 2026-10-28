package auth

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/bcrypt"
)

// ResetTTL — сколько живёт ссылка сброса, отправленная ботом.
const ResetTTL = 30 * time.Minute

var ErrBadReset = errors.New("ссылка для сброса пароля неверна или устарела")

// RequestReset создаёт одноразовый токен сброса для email и в той же
// транзакции вызывает send (поставить сообщение в очередь, напечатать
// ссылку). Старые токены пользователя удаляются: действует только последний.
// Нет такого email — (false, nil), чтобы вызывающий не выдавал этого наружу.
func (s *Service) RequestReset(ctx context.Context, email string, ttl time.Duration,
	send func(tx pgx.Tx, userID int64, token string) error) (bool, error) {
	found := false
	err := pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		var userID int64
		err := tx.QueryRow(ctx, `SELECT id FROM users WHERE lower(email) = lower($1)`, strings.TrimSpace(email)).Scan(&userID)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		found = true
		if _, err := tx.Exec(ctx, `DELETE FROM password_resets WHERE user_id = $1 OR expires_at <= $2`, userID, s.Now()); err != nil {
			return err
		}
		token := NewToken()
		if _, err := tx.Exec(ctx, `INSERT INTO password_resets (token_hash, user_id, expires_at) VALUES ($1, $2, $3)`,
			hashToken(token), userID, s.Now().Add(ttl)); err != nil {
			return err
		}
		return send(tx, userID, token)
	})
	return found && err == nil, err
}

// ResetUser — владелец живого токена сброса (для страницы с формой).
func (s *Service) ResetUser(ctx context.Context, token string) (*User, error) {
	var id int64
	err := s.DB.QueryRow(ctx, `SELECT user_id FROM password_resets WHERE token_hash = $1 AND expires_at > $2`,
		hashToken(token), s.Now()).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrBadReset
	}
	if err != nil {
		return nil, err
	}
	return s.UserByID(ctx, id)
}

// ResetPassword ставит новый пароль по токену. Токен одноразовый; все токены
// сброса и все сессии пользователя удаляются — как при смене пароля.
func (s *Service) ResetPassword(ctx context.Context, token, password string) (userID int64, err error) {
	if ValidatePassword(password) != "" {
		return 0, ErrInvalidInput
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return 0, err
	}
	err = pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `SELECT user_id FROM password_resets WHERE token_hash = $1 AND expires_at > $2 FOR UPDATE`,
			hashToken(token), s.Now()).Scan(&userID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrBadReset
		}
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE users SET password_hash = $2 WHERE id = $1`, userID, string(hash)); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM password_resets WHERE user_id = $1`, userID); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `DELETE FROM sessions WHERE user_id = $1`, userID)
		return err
	})
	return userID, err
}
