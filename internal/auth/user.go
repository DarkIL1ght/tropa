// Package auth — регистрация, вход, сессии в базе и CSRF-защита форм.
package auth

import (
	"net/mail"
	"strings"
	"time"
	"unicode/utf8"
)

type User struct {
	ID        int64
	Email     string
	Name      string
	Role      string
	CreatedAt time.Time
	// Оформление сайта: стиль (Styles) и тема (Schemes).
	Style  string
	Scheme string
	// Verified — email подтверждён кодом из письма; без этого аккаунт только смотрит.
	Verified bool
}

type Option struct{ Value, Label string }

func validOption(opts []Option, v string) bool {
	for _, o := range opts {
		if o.Value == v {
			return true
		}
	}
	return false
}

func (u *User) IsAdmin() bool { return u != nil && u.Role == "admin" }

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
