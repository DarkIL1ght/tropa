package web

import (
	"errors"
	"net/http"
	"strings"

	"tropa/internal/auth"
)

// flashText — тексты коротких сообщений по коду из cookie.
var flashText = map[string]string{
	"welcome": "Добро пожаловать в Tropa!",
	"login":   "Вы вошли.",
	"logout":  "Вы вышли из аккаунта.",
}

type accountForm struct {
	Email  string
	Name   string
	Next   string
	Errors map[string]string
}

func (s *Server) home(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, http.StatusOK, "home.html", nil)
}

func (s *Server) registerForm(w http.ResponseWriter, r *http.Request) {
	if auth.CurrentUser(r.Context()) != nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	s.render(w, r, http.StatusOK, "register.html", accountForm{})
}

func (s *Server) register(w http.ResponseWriter, r *http.Request) {
	f := accountForm{
		Email: strings.TrimSpace(r.PostFormValue("email")),
		Name:  strings.TrimSpace(r.PostFormValue("name")),
	}
	password := r.PostFormValue("password")
	f.Errors = auth.ValidateRegistration(f.Email, f.Name, password)
	if password != r.PostFormValue("password2") && f.Errors["password"] == "" {
		f.Errors["password2"] = "Пароли не совпадают"
	}
	if len(f.Errors) > 0 {
		s.render(w, r, http.StatusUnprocessableEntity, "register.html", f)
		return
	}
	u, err := s.auth.Register(r.Context(), f.Email, f.Name, password)
	if errors.Is(err, auth.ErrEmailTaken) {
		f.Errors["email"] = err.Error()
		s.render(w, r, http.StatusUnprocessableEntity, "register.html", f)
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if err := s.startSession(w, r, u.ID); err != nil {
		s.serverError(w, r, err)
		return
	}
	flash(w, "welcome")
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) loginForm(w http.ResponseWriter, r *http.Request) {
	next := auth.SafeNext(r.URL.Query().Get("next"))
	if auth.CurrentUser(r.Context()) != nil {
		http.Redirect(w, r, next, http.StatusSeeOther)
		return
	}
	s.render(w, r, http.StatusOK, "login.html", accountForm{Next: next})
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	f := accountForm{
		Email: strings.TrimSpace(r.PostFormValue("email")),
		Next:  auth.SafeNext(r.PostFormValue("next")),
	}
	u, err := s.auth.Authenticate(r.Context(), f.Email, r.PostFormValue("password"))
	if errors.Is(err, auth.ErrBadLogin) {
		f.Errors = map[string]string{"form": err.Error()}
		s.render(w, r, http.StatusUnauthorized, "login.html", f)
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if err := s.startSession(w, r, u.ID); err != nil {
		s.serverError(w, r, err)
		return
	}
	flash(w, "login")
	http.Redirect(w, r, f.Next, http.StatusSeeOther)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(auth.SessionCookie); err == nil {
		if err := s.auth.DeleteSession(r.Context(), c.Value); err != nil {
			s.serverError(w, r, err)
			return
		}
	}
	auth.ClearSessionCookie(w, s.cfg.Secure)
	flash(w, "logout")
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, http.StatusOK, "me.html", nil)
}

// startSession открывает новую сессию; старую cookie (если была)
// закрывает, чтобы не копились сессии при повторных входах.
func (s *Server) startSession(w http.ResponseWriter, r *http.Request, userID int64) error {
	if c, err := r.Cookie(auth.SessionCookie); err == nil {
		s.auth.DeleteSession(r.Context(), c.Value)
	}
	token, expires, err := s.auth.CreateSession(r.Context(), userID)
	if err != nil {
		return err
	}
	auth.SetSessionCookie(w, token, expires, s.cfg.Secure)
	return nil
}
