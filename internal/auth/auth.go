// Package auth — регистрация, вход, сессии в базе и CSRF-защита форм.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
)

// SessionTTL — сколько живёт сессия без повторного входа.
const SessionTTL = 30 * 24 * time.Hour

var (
	ErrEmailTaken   = errors.New("этот email уже зарегистрирован")
	ErrBadLogin     = errors.New("неверный email или пароль")
	ErrNoSession    = errors.New("сессия не найдена")
	ErrInvalidInput = errors.New("неверные данные")
	ErrNoUser       = errors.New("пользователь не найден")
	ErrBadPassword  = errors.New("неверный текущий пароль")
)

type User struct {
	ID        int64
	Email     string
	Name      string
	Role      string
	CreatedAt time.Time
}

func (u *User) IsAdmin() bool { return u != nil && u.Role == "admin" }

// Service — операции с пользователями и сессиями. Now подменяется в тестах.
type Service struct {
	DB  *pgxpool.Pool
	Now func() time.Time
}

func New(db *pgxpool.Pool) *Service {
	return &Service{DB: db, Now: time.Now}
}

// ValidateRegistration проверяет поля формы регистрации и возвращает
// ошибки по полям (пустая карта — всё в порядке).
func ValidateRegistration(email, name, password string) map[string]string {
	errs := map[string]string{}
	if a, err := mail.ParseAddress(email); err != nil || a.Address != email {
		errs["email"] = "Введите корректный email"
	}
	if e := ValidateName(name); e != "" {
		errs["name"] = e
	}
	if e := ValidatePassword(password); e != "" {
		errs["password"] = e
	}
	return errs
}

// ValidateName — текст ошибки для имени или пустая строка.
func ValidateName(name string) string {
	if n := utf8.RuneCountInString(strings.TrimSpace(name)); n < 2 || n > 60 {
		return "Имя — от 2 до 60 символов"
	}
	return ""
}

// ValidatePassword — текст ошибки для нового пароля или пустая строка.
func ValidatePassword(password string) string {
	if n := utf8.RuneCountInString(password); n < 8 {
		return "Пароль — не короче 8 символов"
	} else if len(password) > 72 {
		// bcrypt молча обрезает всё длиннее 72 байт
		return "Пароль слишком длинный"
	}
	return ""
}

// Register создаёт пользователя. Поля должны пройти ValidateRegistration.
func (s *Service) Register(ctx context.Context, email, name, password string) (*User, error) {
	email = strings.TrimSpace(email)
	name = strings.TrimSpace(name)
	if len(ValidateRegistration(email, name, password)) > 0 {
		return nil, ErrInvalidInput
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return nil, err
	}
	u := &User{Email: email, Name: name}
	err = s.DB.QueryRow(ctx,
		`INSERT INTO users (email, name, password_hash) VALUES ($1, $2, $3)
		 RETURNING id, role, created_at`,
		email, name, string(hash)).Scan(&u.ID, &u.Role, &u.CreatedAt)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return nil, ErrEmailTaken
	}
	if err != nil {
		return nil, fmt.Errorf("создание пользователя: %w", err)
	}
	return u, nil
}

// dummyHash сравнивается, когда email не найден, чтобы по времени ответа
// нельзя было узнать, зарегистрирован ли адрес.
var dummyHash, _ = bcrypt.GenerateFromPassword([]byte("tropa-dummy-password"), bcrypt.DefaultCost)

// Authenticate проверяет email и пароль.
func (s *Service) Authenticate(ctx context.Context, email, password string) (*User, error) {
	u := &User{}
	var hash string
	err := s.DB.QueryRow(ctx,
		`SELECT id, email, name, role, created_at, password_hash FROM users WHERE lower(email) = lower($1)`,
		strings.TrimSpace(email)).Scan(&u.ID, &u.Email, &u.Name, &u.Role, &u.CreatedAt, &hash)
	if errors.Is(err, pgx.ErrNoRows) {
		bcrypt.CompareHashAndPassword(dummyHash, []byte(password))
		return nil, ErrBadLogin
	}
	if err != nil {
		return nil, err
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) != nil {
		return nil, ErrBadLogin
	}
	return u, nil
}

// UserByID — пользователь по id (для публичного профиля); ErrNoUser, если нет.
func (s *Service) UserByID(ctx context.Context, id int64) (*User, error) {
	u := &User{}
	err := s.DB.QueryRow(ctx, `SELECT id, email, name, role, created_at FROM users WHERE id = $1`, id).
		Scan(&u.ID, &u.Email, &u.Name, &u.Role, &u.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNoUser
	}
	return u, err
}

// Rename меняет имя; оно должно пройти ValidateName.
func (s *Service) Rename(ctx context.Context, userID int64, name string) error {
	name = strings.TrimSpace(name)
	if ValidateName(name) != "" {
		return ErrInvalidInput
	}
	_, err := s.DB.Exec(ctx, `UPDATE users SET name = $2 WHERE id = $1`, userID, name)
	return err
}

// ChangePassword меняет пароль, если current совпадает с нынешним, и закрывает
// все сессии пользователя: украденная cookie после смены пароля не работает.
// Вызывающий открывает текущему браузеру новую сессию.
func (s *Service) ChangePassword(ctx context.Context, userID int64, current, password string) error {
	if ValidatePassword(password) != "" {
		return ErrInvalidInput
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	return pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		var old string
		// FOR UPDATE: две одновременные смены с одним текущим паролем не пройдут обе
		err := tx.QueryRow(ctx, `SELECT password_hash FROM users WHERE id = $1 FOR UPDATE`, userID).Scan(&old)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNoUser
		}
		if err != nil {
			return err
		}
		if bcrypt.CompareHashAndPassword([]byte(old), []byte(current)) != nil {
			return ErrBadPassword
		}
		if _, err := tx.Exec(ctx, `UPDATE users SET password_hash = $2 WHERE id = $1`, userID, string(hash)); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `DELETE FROM sessions WHERE user_id = $1`, userID)
		return err
	})
}

// NewToken — случайный токен для cookie (256 бит).
func NewToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand на поддерживаемых ОС не возвращает ошибок
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func hashToken(token string) []byte {
	h := sha256.Sum256([]byte(token))
	return h[:]
}

// CreateSession открывает сессию и возвращает токен для cookie.
func (s *Service) CreateSession(ctx context.Context, userID int64) (token string, expires time.Time, err error) {
	token = NewToken()
	expires = s.Now().Add(SessionTTL)
	_, err = s.DB.Exec(ctx,
		`INSERT INTO sessions (token_hash, user_id, expires_at) VALUES ($1, $2, $3)`,
		hashToken(token), userID, expires)
	return token, expires, err
}

// UserBySession возвращает владельца живой сессии.
func (s *Service) UserBySession(ctx context.Context, token string) (*User, error) {
	if token == "" {
		return nil, ErrNoSession
	}
	u := &User{}
	err := s.DB.QueryRow(ctx,
		`SELECT u.id, u.email, u.name, u.role, u.created_at
		   FROM sessions s JOIN users u ON u.id = s.user_id
		  WHERE s.token_hash = $1 AND s.expires_at > $2`,
		hashToken(token), s.Now()).Scan(&u.ID, &u.Email, &u.Name, &u.Role, &u.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNoSession
	}
	return u, err
}

// DeleteSession закрывает сессию (выход).
func (s *Service) DeleteSession(ctx context.Context, token string) error {
	_, err := s.DB.Exec(ctx, `DELETE FROM sessions WHERE token_hash = $1`, hashToken(token))
	return err
}

// SetRole меняет роль пользователя по email (команда make-admin).
func (s *Service) SetRole(ctx context.Context, email, role string) error {
	if role != "user" && role != "admin" {
		return ErrInvalidInput
	}
	tag, err := s.DB.Exec(ctx, `UPDATE users SET role = $2 WHERE lower(email) = lower($1)`, strings.TrimSpace(email), role)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("пользователь %s не найден — сначала зарегистрируйтесь на сайте", email)
	}
	return nil
}

// PurgeExpired удаляет просроченные сессии; вызывается фоново.
func (s *Service) PurgeExpired(ctx context.Context) (int64, error) {
	tag, err := s.DB.Exec(ctx, `DELETE FROM sessions WHERE expires_at <= $1`, s.Now())
	return tag.RowsAffected(), err
}
