package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"tropa/internal/auth"
)

// verifyView — страница ввода кода из письма.
type verifyView struct {
	Email string
	Next  string
	Error string
}

// sendCode отправляет пользователю письмо с новым кодом подтверждения.
func (s *Server) sendCode(r *http.Request, u *auth.User) error {
	return s.auth.SendCode(r.Context(), u, func(code string) error {
		body := fmt.Sprintf("Ваш код подтверждения Tropa: %s\n\nКод действует %d минут. Если вы не регистрировались в Tropa, просто удалите это письмо.",
			code, int(auth.CodeTTL.Minutes()))
		return s.mail.Send(r.Context(), u.Email, "Код подтверждения Tropa: "+code, body)
	})
}

func (s *Server) verifyForm(w http.ResponseWriter, r *http.Request) {
	u := auth.CurrentUser(r.Context())
	next := auth.SafeNext(r.URL.Query().Get("next"))
	if u.Verified {
		http.Redirect(w, r, next, http.StatusSeeOther)
		return
	}
	s.render(w, r, http.StatusOK, "verify.html", verifyView{Email: u.Email, Next: next})
}

var codeErrors = []error{auth.ErrBadCode, auth.ErrCodeExpired, auth.ErrCodeAttempts}

func (s *Server) verify(w http.ResponseWriter, r *http.Request) {
	u := auth.CurrentUser(r.Context())
	v := verifyView{Email: u.Email, Next: auth.SafeNext(r.PostFormValue("next"))}
	if u.Verified {
		http.Redirect(w, r, v.Next, http.StatusSeeOther)
		return
	}
	if ok, retry := s.limits.verify.allow(strconv.FormatInt(u.ID, 10)); !ok {
		v.Error = tooMany(w, retry)
		s.render(w, r, http.StatusTooManyRequests, "verify.html", v)
		return
	}
	err := s.auth.VerifyEmail(r.Context(), u.ID, r.PostFormValue("code"))
	for _, e := range codeErrors {
		if errors.Is(err, e) {
			v.Error = err.Error()
			s.render(w, r, http.StatusUnprocessableEntity, "verify.html", v)
			return
		}
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	flash(w, "email_verified")
	http.Redirect(w, r, v.Next, http.StatusSeeOther)
}

func (s *Server) resendCode(w http.ResponseWriter, r *http.Request) {
	u := auth.CurrentUser(r.Context())
	next := auth.SafeNext(r.PostFormValue("next"))
	back := "/verify?next=" + url.QueryEscape(next)
	if ok, retry := s.limits.resend.allow(strconv.FormatInt(u.ID, 10)); !ok {
		http.Error(w, tooMany(w, retry), http.StatusTooManyRequests)
		return
	}
	switch err := s.sendCode(r, u); {
	case errors.Is(err, auth.ErrVerified):
		http.Redirect(w, r, next, http.StatusSeeOther)
		return
	case errors.Is(err, auth.ErrCodeTooSoon):
		flash(w, "code_too_soon")
	case err != nil:
		s.serverError(w, r, err)
		return
	default:
		flash(w, "code_sent")
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// verified пускает только вошедших с подтверждённой почтой. Остальным —
// страница ввода кода (гостю — вход). Запросы chat.js получают JSON-ошибку.
func (s *Server) verified(h http.HandlerFunc) http.Handler {
	return auth.RequireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if auth.CurrentUser(r.Context()).Verified {
			h(w, r)
			return
		}
		if strings.Contains(r.Header.Get("Accept"), "application/json") {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			json.NewEncoder(w).Encode(map[string]string{"error": "Сначала подтвердите почту."})
			return
		}
		next := r.URL.RequestURI()
		if r.Method != http.MethodGet {
			next = refererPath(r)
		}
		flash(w, "verify_first")
		http.Redirect(w, r, "/verify?next="+url.QueryEscape(auth.SafeNext(next)), http.StatusSeeOther)
	}))
}

// refererPath — страница нашего сайта, с которой отправили форму (после
// подтверждения вернёмся на неё), или "/".
func refererPath(r *http.Request) string {
	ref, err := url.Parse(r.Referer())
	if err != nil || ref.Host != r.Host {
		return "/"
	}
	return ref.RequestURI()
}
