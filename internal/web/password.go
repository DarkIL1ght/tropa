package web

import (
	"errors"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"

	"tropa/internal/auth"
	"tropa/internal/notify"
)

type forgotView struct {
	Email string
	// Sent — запрос принят (ответ одинаковый, есть такой email или нет).
	Sent bool
	// Telegram — сброс через бота доступен на сайте.
	Telegram bool
	Errors   map[string]string
}

func (s *Server) forgotForm(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, http.StatusOK, "forgot.html", forgotView{Telegram: s.notify.Enabled()})
}

// forgot отправляет ссылку сброса в Telegram. Ответ не говорит, есть ли
// такой аккаунт и подключён ли у него Telegram: иначе по форме можно
// перебирать, кто зарегистрирован.
func (s *Server) forgot(w http.ResponseWriter, r *http.Request) {
	v := forgotView{Email: strings.TrimSpace(r.PostFormValue("email")), Telegram: s.notify.Enabled()}
	okIP, retryIP := s.limits.forgotIP.allow(s.clientIP(r))
	okEmail, retryEmail := s.limits.forgotEmail.allow(strings.ToLower(v.Email))
	if !okIP || !okEmail {
		v.Errors = map[string]string{"form": tooMany(w, max(retryIP, retryEmail))}
		s.render(w, r, http.StatusTooManyRequests, "forgot.html", v)
		return
	}
	if v.Telegram && v.Email != "" {
		_, err := s.auth.RequestReset(r.Context(), v.Email, auth.ResetTTL, func(tx pgx.Tx, userID int64, token string) error {
			// без Telegram у пользователя Enqueue ничего не ставит, токен просто истечёт
			return notify.Enqueue(r.Context(), tx, userID, "password_reset", resetText(s.cfg.SiteURL+"/password/reset/"+token), "")
		})
		if err != nil {
			s.serverError(w, r, err)
			return
		}
	}
	v.Sent = true
	s.render(w, r, http.StatusOK, "forgot.html", v)
}

func resetText(link string) string {
	return "Сброс пароля в Tropa. Ссылка действует 30 минут:\n" + link +
		"\n\nЕсли вы не просили сбросить пароль — просто не открывайте ссылку."
}

type resetView struct {
	Name   string
	Token  string
	Bad    bool
	Errors map[string]string
}

func (s *Server) resetForm(w http.ResponseWriter, r *http.Request) {
	// токен в адресе — не отдаём его в Referer даже своим страницам
	w.Header().Set("Referrer-Policy", "no-referrer")
	token := r.PathValue("token")
	u, err := s.auth.ResetUser(r.Context(), token)
	if errors.Is(err, auth.ErrBadReset) {
		s.render(w, r, http.StatusNotFound, "reset.html", resetView{Bad: true})
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "reset.html", resetView{Name: u.Name, Token: token})
}

func (s *Server) reset(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Referrer-Policy", "no-referrer")
	v := resetView{Token: r.PathValue("token"), Errors: map[string]string{}}
	password := r.PostFormValue("password")
	if e := auth.ValidatePassword(password); e != "" {
		v.Errors["password"] = e
	} else if password != r.PostFormValue("password2") {
		v.Errors["password2"] = "Пароли не совпадают"
	}
	if len(v.Errors) > 0 {
		if u, err := s.auth.ResetUser(r.Context(), v.Token); err == nil {
			v.Name = u.Name
		} else {
			v.Bad = true
		}
		s.render(w, r, http.StatusUnprocessableEntity, "reset.html", v)
		return
	}
	userID, err := s.auth.ResetPassword(r.Context(), v.Token, password)
	if errors.Is(err, auth.ErrBadReset) {
		s.render(w, r, http.StatusNotFound, "reset.html", resetView{Bad: true})
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if err := s.startSession(w, r, userID); err != nil {
		s.serverError(w, r, err)
		return
	}
	flash(w, "password_reset")
	http.Redirect(w, r, "/", http.StatusSeeOther)
}
