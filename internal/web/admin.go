package web

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"tropa/internal/auth"
	"tropa/internal/meetups"
)

// adminResetTTL — ссылка из админки живёт сутки: её пересылают вручную.
const adminResetTTL = 24 * time.Hour

// requireAdmin пускает только админов; остальным — обычная 404, чтобы
// админка не светилась (гостя сначала отправляем на вход).
func (s *Server) requireAdmin(next http.HandlerFunc) http.Handler {
	return auth.RequireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !auth.CurrentUser(r.Context()).IsAdmin() {
			s.notFound(w, r)
			return
		}
		next(w, r)
	}))
}

type adminUsersView struct {
	Stats auth.AdminStats
	Users []auth.AdminUser
	Query string
	Limit int
	// Reset — только что созданная ссылка сброса (показывается один раз).
	Reset     string
	ResetName string
	Error     string
}

func (s *Server) adminUsers(w http.ResponseWriter, r *http.Request) {
	s.renderAdminUsers(w, r, http.StatusOK, adminUsersView{Query: r.URL.Query().Get("q")})
}

func (s *Server) renderAdminUsers(w http.ResponseWriter, r *http.Request, status int, v adminUsersView) {
	var err error
	if v.Stats, err = s.auth.AdminStats(r.Context()); err != nil {
		s.serverError(w, r, err)
		return
	}
	if v.Users, err = s.auth.ListUsers(r.Context(), v.Query); err != nil {
		s.serverError(w, r, err)
		return
	}
	v.Limit = auth.AdminLimit
	s.render(w, r, status, "admin_users.html", v)
}

// adminUser — пользователь из адреса /admin/users/{id}/…
func (s *Server) adminUser(w http.ResponseWriter, r *http.Request) (*auth.User, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		s.notFound(w, r)
		return nil, false
	}
	u, err := s.auth.UserByID(r.Context(), id)
	if errors.Is(err, auth.ErrNoUser) {
		s.notFound(w, r)
		return nil, false
	}
	if err != nil {
		s.serverError(w, r, err)
		return nil, false
	}
	return u, true
}

// adminReset создаёт ссылку сброса пароля и показывает её админу — он
// пересылает её сам (нужно тем, у кого не подключён Telegram).
func (s *Server) adminReset(w http.ResponseWriter, r *http.Request) {
	u, ok := s.adminUser(w, r)
	if !ok {
		return
	}
	v := adminUsersView{ResetName: u.Name}
	_, err := s.auth.RequestReset(r.Context(), u.Email, adminResetTTL, func(_ pgx.Tx, _ int64, token string) error {
		v.Reset = s.cfg.SiteURL + "/password/reset/" + token
		return nil
	})
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	// ссылка в теле страницы — не отдаём адрес админки в Referer по ссылкам с неё
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Cache-Control", "no-store")
	s.renderAdminUsers(w, r, http.StatusOK, v)
}

func (s *Server) adminRole(w http.ResponseWriter, r *http.Request) {
	u, ok := s.adminUser(w, r)
	if !ok {
		return
	}
	err := s.auth.SetRoleByID(r.Context(), u.ID, r.PostFormValue("role"))
	switch {
	case errors.Is(err, auth.ErrLastAdmin):
		s.renderAdminUsers(w, r, http.StatusConflict, adminUsersView{Error: err.Error()})
		return
	case errors.Is(err, auth.ErrInvalidInput):
		s.renderAdminUsers(w, r, http.StatusUnprocessableEntity, adminUsersView{Error: "Неизвестная роль"})
		return
	case err != nil:
		s.serverError(w, r, err)
		return
	}
	flash(w, "role_saved")
	http.Redirect(w, r, "/admin", http.StatusSeeOther)
}

// adminMeetupsLimit — сколько последних встреч показывает админка.
const adminMeetupsLimit = 100

type adminMeetupsView struct {
	Meetups []*meetups.Meetup
	Limit   int
}

func (s *Server) adminMeetups(w http.ResponseWriter, r *http.Request) {
	list, err := s.meetups.Recent(r.Context(), adminMeetupsLimit)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "admin_meetups.html", adminMeetupsView{Meetups: list, Limit: adminMeetupsLimit})
}
