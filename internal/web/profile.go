package web

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"tropa/internal/auth"
	"tropa/internal/meetups"
)

type settingsView struct {
	Name   string
	Errors map[string]string
}

func (s *Server) settings(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, http.StatusOK, "settings.html", settingsView{Name: auth.CurrentUser(r.Context()).Name})
}

func (s *Server) rename(w http.ResponseWriter, r *http.Request) {
	u := auth.CurrentUser(r.Context())
	v := settingsView{Name: strings.TrimSpace(r.PostFormValue("name")), Errors: map[string]string{}}
	if e := auth.ValidateName(v.Name); e != "" {
		v.Errors["name"] = e
		s.render(w, r, http.StatusUnprocessableEntity, "settings.html", v)
		return
	}
	if err := s.auth.Rename(r.Context(), u.ID, v.Name); err != nil {
		s.serverError(w, r, err)
		return
	}
	flash(w, "name_saved")
	http.Redirect(w, r, "/me/settings", http.StatusSeeOther)
}

func (s *Server) changePassword(w http.ResponseWriter, r *http.Request) {
	u := auth.CurrentUser(r.Context())
	v := settingsView{Name: u.Name, Errors: map[string]string{}}
	password := r.PostFormValue("password")
	if e := auth.ValidatePassword(password); e != "" {
		v.Errors["password"] = e
	} else if password != r.PostFormValue("password2") {
		v.Errors["password2"] = "Пароли не совпадают"
	}
	if len(v.Errors) == 0 {
		err := s.auth.ChangePassword(r.Context(), u.ID, r.PostFormValue("current"), password)
		switch {
		case errors.Is(err, auth.ErrBadPassword):
			v.Errors["current"] = "Неверный текущий пароль"
		case err != nil:
			s.serverError(w, r, err)
			return
		}
	}
	if len(v.Errors) > 0 {
		s.render(w, r, http.StatusUnprocessableEntity, "settings.html", v)
		return
	}
	// все сессии закрыты, этому браузеру — новая
	if err := s.startSession(w, r, u.ID); err != nil {
		s.serverError(w, r, err)
		return
	}
	flash(w, "password_changed")
	http.Redirect(w, r, "/me/settings", http.StatusSeeOther)
}

type userView struct {
	Person   *auth.User
	Stats    meetups.Stats
	Upcoming []*meetups.Meetup
	Own      bool // смотрит сам владелец
}

// showUser — публичная страница пользователя: без email и без встреч «для друзей».
func (s *Server) showUser(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		s.notFound(w, r)
		return
	}
	v := userView{}
	v.Person, err = s.auth.UserByID(r.Context(), id)
	if errors.Is(err, auth.ErrNoUser) {
		s.notFound(w, r)
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if me := auth.CurrentUser(r.Context()); me != nil && me.ID == id {
		v.Own = true
	}
	if v.Stats, err = s.meetups.Stats(r.Context(), id); err != nil {
		s.serverError(w, r, err)
		return
	}
	if v.Upcoming, err = s.meetups.PublicByOrganizer(r.Context(), id); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "user.html", v)
}
