package auth

import (
	"context"
	"crypto/subtle"
	"net/http"
	"time"
)

const (
	SessionCookie = "tropa_session"
	CSRFCookie    = "tropa_csrf"
	CSRFField     = "csrf_token"
)

type ctxKey int

const (
	userKey ctxKey = iota
	csrfKey
)

// CurrentUser — пользователь из сессии или nil для гостя.
func CurrentUser(ctx context.Context) *User {
	u, _ := ctx.Value(userKey).(*User)
	return u
}

// CSRFToken — токен для скрытого поля форм текущего запроса.
func CSRFToken(ctx context.Context) string {
	t, _ := ctx.Value(csrfKey).(string)
	return t
}

// Middleware кладёт в контекст пользователя и CSRF-токен.
//
// CSRF — схема double-submit: случайный токен лежит в HttpOnly-cookie
// и дублируется в скрытом поле формы. Чужой сайт не может прочитать
// cookie, поэтому не подставит верное значение поля. SameSite=Lax —
// второй рубеж. Все небезопасные методы без совпадения — 403.
func (s *Service) Middleware(secure bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()

			csrf := ""
			if c, err := r.Cookie(CSRFCookie); err == nil && len(c.Value) >= 32 {
				csrf = c.Value
			} else {
				csrf = NewToken()
				http.SetCookie(w, &http.Cookie{
					Name: CSRFCookie, Value: csrf, Path: "/",
					HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode,
				})
			}
			ctx = context.WithValue(ctx, csrfKey, csrf)

			switch r.Method {
			case http.MethodGet, http.MethodHead, http.MethodOptions:
			default:
				got := r.PostFormValue(CSRFField)
				if got == "" {
					got = r.Header.Get("X-CSRF-Token")
				}
				if subtle.ConstantTimeCompare([]byte(got), []byte(csrf)) != 1 {
					http.Error(w, "Форма устарела, обновите страницу", http.StatusForbidden)
					return
				}
			}

			if c, err := r.Cookie(SessionCookie); err == nil {
				if u, err := s.UserBySession(ctx, c.Value); err == nil {
					ctx = context.WithValue(ctx, userKey, u)
				}
			}
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// SetSessionCookie выставляет cookie входа.
func SetSessionCookie(w http.ResponseWriter, token string, expires time.Time, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name: SessionCookie, Value: token, Path: "/", Expires: expires,
		HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode,
	})
}

// ClearSessionCookie удаляет cookie входа.
func ClearSessionCookie(w http.ResponseWriter, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name: SessionCookie, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode,
	})
}

// RequireUser пускает только вошедших; гостя отправляет на вход
// с возвратом на исходную страницу.
func RequireUser(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if CurrentUser(r.Context()) == nil {
			http.Redirect(w, r, "/login?next="+safeNext(r.URL.RequestURI()), http.StatusSeeOther)
			return
		}
		next.ServeHTTP(w, r)
	})
}
