package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	// CodeTTL — сколько живёт код из письма.
	CodeTTL = 15 * time.Minute
	// CodeResend — не чаще одного письма за этот промежуток.
	CodeResend = time.Minute
	// CodeAttempts — сколько раз можно ошибиться с одним кодом; потом нужен новый.
	CodeAttempts = 5
	// UnverifiedTTL — через сколько удаляется аккаунт, так и не подтвердивший почту.
	UnverifiedTTL = 7 * 24 * time.Hour
)

var (
	ErrVerified     = errors.New("почта уже подтверждена")
	ErrCodeTooSoon  = errors.New("код уже отправлен — новый можно запросить через минуту")
	ErrBadCode      = errors.New("неверный код")
	ErrCodeExpired  = errors.New("код устарел — запросите новый")
	ErrCodeAttempts = errors.New("слишком много неверных попыток — запросите новый код")
)

func hashCode(userID int64, code string) []byte {
	h := sha256.Sum256(fmt.Appendf(nil, "%d:%s", userID, code))
	return h[:]
}

func newCode() string {
	n, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
	if err != nil {
		panic(err) // crypto/rand на поддерживаемых ОС не возвращает ошибок
	}
	return fmt.Sprintf("%06d", n.Int64())
}

// SendCode создаёт новый код подтверждения (старый перестаёт действовать) и
// передаёт его send. send вызывается в транзакции: не ушло письмо — код не
// сохранится и повторить можно сразу.
func (s *Service) SendCode(ctx context.Context, u *User, send func(code string) error) error {
	if u.Verified {
		return ErrVerified
	}
	return pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		// строка пользователя под блокировкой: два одновременных запроса не отправят два письма
		var verified bool
		err := tx.QueryRow(ctx, `SELECT email_verified_at IS NOT NULL FROM users WHERE id = $1 FOR UPDATE`, u.ID).Scan(&verified)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNoUser
		}
		if err != nil {
			return err
		}
		if verified {
			return ErrVerified
		}
		var sent time.Time
		err = tx.QueryRow(ctx, `SELECT sent_at FROM email_codes WHERE user_id = $1`, u.ID).Scan(&sent)
		if err == nil && s.Now().Sub(sent) < CodeResend {
			return ErrCodeTooSoon
		}
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		code, now := newCode(), s.Now()
		if _, err := tx.Exec(ctx, `
			INSERT INTO email_codes (user_id, code_hash, expires_at, sent_at) VALUES ($1, $2, $3, $4)
			ON CONFLICT (user_id) DO UPDATE SET code_hash = $2, expires_at = $3, sent_at = $4, attempts = 0`,
			u.ID, hashCode(u.ID, code), now.Add(CodeTTL), now); err != nil {
			return err
		}
		return send(code)
	})
}

// VerifyEmail подтверждает почту кодом из письма. Неверная попытка
// засчитывается, после CodeAttempts код больше не принимается.
func (s *Service) VerifyEmail(ctx context.Context, userID int64, code string) error {
	code = strings.Map(func(r rune) rune {
		if r == ' ' || r == '-' {
			return -1
		}
		return r
	}, code)
	var result error
	err := pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		var hash []byte
		var expires time.Time
		var attempts int
		err := tx.QueryRow(ctx, `SELECT code_hash, expires_at, attempts FROM email_codes WHERE user_id = $1 FOR UPDATE`, userID).
			Scan(&hash, &expires, &attempts)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			result = ErrCodeExpired
			return nil
		case err != nil:
			return err
		case attempts >= CodeAttempts:
			result = ErrCodeAttempts
			return nil
		case !s.Now().Before(expires):
			result = ErrCodeExpired
			return nil
		}
		if subtle.ConstantTimeCompare(hash, hashCode(userID, code)) != 1 {
			// попытку сохраняем: ошибка возвращается после коммита
			result = ErrBadCode
			_, err := tx.Exec(ctx, `UPDATE email_codes SET attempts = attempts + 1 WHERE user_id = $1`, userID)
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE users SET email_verified_at = $2 WHERE id = $1`, userID, s.Now()); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `DELETE FROM email_codes WHERE user_id = $1`, userID)
		return err
	})
	if err != nil {
		return err
	}
	return result
}

// PurgeUnverified удаляет аккаунты, которые не подтвердили почту за
// UnverifiedTTL: они ничего не успели создать, а адрес освобождается.
func (s *Service) PurgeUnverified(ctx context.Context) (int64, error) {
	tag, err := s.DB.Exec(ctx, `DELETE FROM users WHERE email_verified_at IS NULL AND created_at <= $1`, s.Now().Add(-UnverifiedTTL))
	return tag.RowsAffected(), err
}
