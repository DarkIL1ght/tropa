package web

import (
	"net/http"

	"tropa/internal/auth"
)

// linkTelegram создаёт одноразовую ссылку на бота и показывает её:
// Telegram откроет чат с ботом, а /start с кодом привяжет аккаунт.
func (s *Server) linkTelegram(w http.ResponseWriter, r *http.Request) {
	if !s.notify.Enabled() {
		http.Redirect(w, r, "/me", http.StatusSeeOther)
		return
	}
	url, err := s.notify.LinkURL(r.Context(), auth.CurrentUser(r.Context()).ID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	// не редирект: CSP form-action не пускает после формы на чужой сайт,
	// поэтому показываем страницу с кнопкой-ссылкой
	s.render(w, r, http.StatusOK, "telegram_link.html", url)
}

func (s *Server) unlinkTelegram(w http.ResponseWriter, r *http.Request) {
	if s.notify.Enabled() {
		if err := s.notify.Unlink(r.Context(), auth.CurrentUser(r.Context()).ID); err != nil {
			s.serverError(w, r, err)
			return
		}
		flash(w, "telegram_off")
	}
	http.Redirect(w, r, "/me", http.StatusSeeOther)
}

type telegramHelpView struct {
	Enabled bool
	BotName string
}

func (s *Server) telegramHelp(w http.ResponseWriter, r *http.Request) {
	v := telegramHelpView{Enabled: s.notify.Enabled()}
	if v.Enabled {
		v.BotName = s.notify.BotName
	}
	s.render(w, r, http.StatusOK, "telegram_help.html", v)
}
