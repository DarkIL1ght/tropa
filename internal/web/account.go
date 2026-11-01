package web

import (
	"errors"
	"net/http"
	"strings"

	"tropa/internal/auth"
	"tropa/internal/meetups"
)

// flashText — тексты коротких сообщений по коду из cookie.
var flashText = map[string]string{
	"welcome": "Добро пожаловать в Tropa!",
	"login":   "Вы вошли.",
	"logout":  "Вы вышли из аккаунта.",

	"name_saved":       "Имя сохранено.",
	"password_changed": "Пароль изменён. На других устройствах нужно войти заново.",
	"appearance_saved": "Оформление сохранено.",
	"role_saved":       "Роль изменена.",
	"password_reset":   "Пароль изменён, вы вошли. На других устройствах нужно войти заново.",

	"meetup_created":   "Встреча создана. Поделитесь ссылкой с друзьями.",
	"meetup_saved":     "Изменения сохранены.",
	"meetup_locked":    "Отменённая или прошедшая встреча не меняется.",
	"meetup_stale":     "Статус встречи уже изменился — вот актуальный.",
	"meetup_open":      "Запись открыта.",
	"meetup_closed":    "Запись закрыта.",
	"meetup_cancelled": "Встреча отменена.",
	"join_confirmed":   "Вы идёте!",
	"join_waitlist":    "Мест нет — вы в очереди. Если место освободится, оно будет ждать вас сутки.",
	"join_offered":     "Для вас освободилось место — подтвердите участие.",
	"join_closed":      "Запись на эту встречу закрыта.",
	"left":             "Вы больше не участвуете во встрече.",
	"leave_organizer":  "Организатор не может выйти из своей встречи — её можно отменить.",
	"offer_gone":       "Предложение места истекло.",
	"telegram_off":     "Уведомления в Telegram отключены.",

	"email_verified": "Почта подтверждена — теперь вам доступно всё.",
	"code_sent":      "Отправили новый код.",
	"code_too_soon":  "Код уже отправлен — новый можно запросить через минуту.",
	"verify_first":   "Сначала подтвердите почту: введите код из письма.",

	"friend_requested": "Заявка отправлена. Когда её примут, вы станете друзьями.",
	"friend_added":     "Теперь вы друзья!",
	"friend_removed":   "Готово.",
	"friend_stale":     "Заявка уже не актуальна — вот как сейчас.",

	"place_suggested": "Спасибо! Место отправлено на проверку — как только его посмотрят, оно появится на карте.",
	"place_published": "Место опубликовано.",
	"place_rejected":  "Место отклонено, автор увидит причину.",
	"place_reviewed":  "Это место уже проверили — вот актуальная очередь.",
	"reason_required": "Напишите причину отказа — от 3 до 500 символов.",
}

type accountForm struct {
	Email  string
	Name   string
	Next   string
	Errors map[string]string
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
	if ok, retry := s.limits.register.allow(s.clientIP(r)); !ok {
		f.Errors = map[string]string{"form": tooMany(w, retry)}
		s.render(w, r, http.StatusTooManyRequests, "register.html", f)
		return
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
	// не ушло письмо — на странице кода можно запросить его ещё раз
	if err := s.sendCode(r, u); err != nil {
		s.log.Error("письмо с кодом", "user", u.ID, "err", err)
	}
	http.Redirect(w, r, "/verify", http.StatusSeeOther)
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
	// подбор пароля: и с одного адреса, и к одному аккаунту с разных
	okIP, retryIP := s.limits.loginIP.allow(s.clientIP(r))
	okEmail, retryEmail := s.limits.loginEmail.allow(strings.ToLower(f.Email))
	if !okIP || !okEmail {
		f.Errors = map[string]string{"form": tooMany(w, max(retryIP, retryEmail))}
		s.render(w, r, http.StatusTooManyRequests, "login.html", f)
		return
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

type meView struct {
	Stats  meetups.Stats
	Mine   []*meetups.Meetup
	Joined []meetups.Joined
	// Telegram — уведомления включены на сайте; Linked — подключены у пользователя.
	Telegram bool
	Linked   bool
	// Incoming — входящие заявки в друзья.
	Incoming int
}

func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	u := auth.CurrentUser(r.Context())
	v := meView{Telegram: s.notify.Enabled()}
	var err error
	if v.Telegram {
		if v.Linked, err = s.notify.Linked(r.Context(), u.ID); err != nil {
			s.serverError(w, r, err)
			return
		}
	}
	if _, v.Incoming, err = s.friends.Counts(r.Context(), u.ID); err != nil {
		s.serverError(w, r, err)
		return
	}
	if v.Stats, err = s.meetups.Stats(r.Context(), u.ID); err != nil {
		s.serverError(w, r, err)
		return
	}
	if v.Mine, err = s.meetups.ByOrganizer(r.Context(), u.ID); err != nil {
		s.serverError(w, r, err)
		return
	}
	if v.Joined, err = s.meetups.ByParticipant(r.Context(), u.ID); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "me.html", v)
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
