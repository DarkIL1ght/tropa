// Package web — HTTP-обработчики, шаблоны и статика.
package web

import (
	"bytes"
	"embed"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"tropa/internal/auth"
	"tropa/internal/dm"
	"tropa/internal/friends"
	"tropa/internal/mail"
	"tropa/internal/meetups"
	"tropa/internal/notify"
	"tropa/internal/places"
	"tropa/internal/weather"
)

//go:embed templates static
var assets embed.FS

type Config struct {
	// Secure — cookie только по HTTPS (включать за прокси с TLS).
	Secure bool
	// TrustProxy — адрес посетителя брать из X-Forwarded-For (сервер за своим прокси).
	TrustProxy bool
	// SiteURL — адрес сайта без / в конце, для ссылок в сообщениях бота.
	SiteURL string
	// RequireVerification — без подтверждённой почты аккаунт только смотрит.
	// Выключено — подтверждение необязательно (письма ещё не доходят до всех).
	RequireVerification bool
}

// Services — бизнес-сервисы, которыми пользуются обработчики.
type Services struct {
	Auth    *auth.Service
	Meetups *meetups.Service
	Places  *places.Service
	// Notify — уведомления в Telegram; nil или без бота — выключены.
	Notify *notify.Service
	// Friends — дружба между пользователями.
	Friends *friends.Service
	// Weather — погода для плашки на карте; nil — без погоды.
	Weather *weather.Client
	// DM — личные сообщения между друзьями.
	DM *dm.Service
	// Mail — письма (коды подтверждения); nil — письма пишутся в лог.
	Mail mail.Sender
}

type Server struct {
	cfg     Config
	auth    *auth.Service
	meetups *meetups.Service
	notify  *notify.Service
	friends *friends.Service
	places  *places.Service
	weather *weather.Client
	dm      *dm.Service
	mail    mail.Sender
	// now — текущее время (подменяется в тестах): от него зависит «не сезон».
	now    func() time.Time
	limits *limits
	chat   *hub[meetups.Message]
	dms    *hub[dm.Message]
	log    *slog.Logger
	pages  map[string]*template.Template
}

func New(cfg Config, svc Services, log *slog.Logger) (*Server, error) {
	s := &Server{cfg: cfg, auth: svc.Auth, meetups: svc.Meetups, notify: svc.Notify, friends: svc.Friends, places: svc.Places, weather: svc.Weather,
		dm: svc.DM, mail: svc.Mail, now: time.Now, limits: newLimits(), chat: newHub[meetups.Message](), dms: newHub[dm.Message](), log: log}
	if s.mail == nil {
		s.mail = mail.LogSender{Log: log}
	}
	if err := s.loadTemplates(); err != nil {
		return nil, err
	}
	return s, nil
}

var funcs = template.FuncMap{
	"date":    func(t time.Time) string { return t.Format("02.01.2006") },
	"when":    when,
	"clock":   clock,
	"plural":  plural,
	"initial": initial,
	"ticket":  ticket,
	"hall":    hall,
	"lower":   func(s string) string { return strings.ToLower(s) },
	"add":     func(a, b int) int { return a + b },
	"sub":     func(a, b int) int { return a - b },
	// dict — параметры для вложенного шаблона: dict "ключ" значение …
	"dict": func(kv ...any) map[string]any {
		m := make(map[string]any, len(kv)/2)
		for i := 0; i+1 < len(kv); i += 2 {
			m[kv[i].(string)] = kv[i+1]
		}
		return m
	},
}

// loadTemplates собирает каждую страницу как layout + своя страница,
// чтобы блоки "title" и "content" не конфликтовали между страницами.
func (s *Server) loadTemplates() error {
	s.pages = map[string]*template.Template{}
	pages, err := fs.Glob(assets, "templates/pages/*.html")
	if err != nil {
		return err
	}
	for _, p := range pages {
		t, err := template.New("layout.html").Funcs(funcs).ParseFS(assets, "templates/layout.html", p)
		if err != nil {
			return fmt.Errorf("шаблон %s: %w", p, err)
		}
		s.pages[p[len("templates/pages/"):]] = t
	}
	return nil
}

// Handler — корневой обработчик со всеми маршрутами и middleware.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	static, _ := fs.Sub(assets, "static")
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(static)))

	mux.HandleFunc("GET /{$}", s.mapPage)
	mux.HandleFunc("GET /api/places", s.apiPlaces)
	mux.HandleFunc("GET /places/{slug}", s.showPlace)
	mux.Handle("GET /places/suggest", s.verified(s.suggestForm))
	mux.Handle("POST /places/suggest", s.verified(s.suggestPlace))
	mux.HandleFunc("GET /register", s.registerForm)
	mux.HandleFunc("POST /register", s.register)
	mux.HandleFunc("GET /login", s.loginForm)
	mux.HandleFunc("POST /login", s.login)
	mux.HandleFunc("POST /logout", s.logout)
	mux.Handle("GET /verify", auth.RequireUser(http.HandlerFunc(s.verifyForm)))
	mux.Handle("POST /verify", auth.RequireUser(http.HandlerFunc(s.verify)))
	mux.Handle("POST /verify/resend", auth.RequireUser(http.HandlerFunc(s.resendCode)))
	mux.HandleFunc("GET /password/forgot", s.forgotForm)
	mux.HandleFunc("POST /password/forgot", s.forgot)
	mux.HandleFunc("GET /password/reset/{token}", s.resetForm)
	mux.HandleFunc("POST /password/reset/{token}", s.reset)
	mux.Handle("GET /me", auth.RequireUser(http.HandlerFunc(s.me)))
	mux.Handle("POST /me/telegram", auth.RequireUser(http.HandlerFunc(s.linkTelegram)))
	mux.Handle("POST /me/telegram/off", auth.RequireUser(http.HandlerFunc(s.unlinkTelegram)))
	mux.HandleFunc("GET /help/telegram", s.telegramHelp)
	mux.Handle("GET /me/settings", auth.RequireUser(http.HandlerFunc(s.settings)))
	mux.Handle("POST /me/name", auth.RequireUser(http.HandlerFunc(s.rename)))
	mux.Handle("POST /me/password", auth.RequireUser(http.HandlerFunc(s.changePassword)))
	mux.Handle("POST /me/appearance", auth.RequireUser(http.HandlerFunc(s.setAppearance)))
	mux.HandleFunc("GET /u/{id}", s.showUser)
	mux.Handle("POST /u/{id}/friend", s.verified(s.friendAction))
	mux.Handle("GET /friends", auth.RequireUser(http.HandlerFunc(s.friendsPage)))
	mux.Handle("GET /messages", auth.RequireUser(http.HandlerFunc(s.inbox)))
	mux.Handle("GET /messages/{id}", auth.RequireUser(http.HandlerFunc(s.conversation)))
	mux.Handle("POST /messages/{id}", s.verified(s.postDirect))
	mux.Handle("GET /messages/{id}/stream", auth.RequireUser(http.HandlerFunc(s.directStream)))

	mux.HandleFunc("GET /meetups", s.meetupList)
	mux.Handle("GET /places/{slug}/meetups/new", s.verified(s.newMeetup))
	mux.Handle("POST /places/{slug}/meetups", s.verified(s.createMeetup))
	mux.HandleFunc("GET /m/{token}", s.showMeetup)
	mux.Handle("GET /m/{token}/edit", auth.RequireUser(http.HandlerFunc(s.editMeetup)))
	mux.Handle("POST /m/{token}/edit", auth.RequireUser(http.HandlerFunc(s.updateMeetup)))
	mux.Handle("POST /m/{token}/status", auth.RequireUser(http.HandlerFunc(s.setMeetupStatus)))
	mux.Handle("POST /m/{token}/join", s.verified(s.joinMeetup))
	mux.Handle("POST /m/{token}/leave", auth.RequireUser(http.HandlerFunc(s.leaveMeetup)))
	mux.Handle("POST /m/{token}/accept", s.verified(s.acceptOffer))
	mux.Handle("POST /m/{token}/messages", s.verified(s.postMessage))
	mux.Handle("GET /m/{token}/stream", auth.RequireUser(http.HandlerFunc(s.stream)))
	mux.Handle("GET /admin", s.requireAdmin(s.adminUsers))
	mux.Handle("POST /admin/users/{id}/reset", s.requireAdmin(s.adminReset))
	mux.Handle("POST /admin/users/{id}/role", s.requireAdmin(s.adminRole))
	mux.Handle("GET /admin/meetups", s.requireAdmin(s.adminMeetups))
	mux.Handle("GET /admin/places", s.requireAdmin(s.adminPlaces))
	mux.Handle("GET /admin/places/{id}/edit", s.requireAdmin(s.adminPlaceEdit))
	mux.Handle("POST /admin/places/{id}/publish", s.requireAdmin(s.adminPublish))
	mux.Handle("POST /admin/places/{id}/reject", s.requireAdmin(s.adminReject))
	// всё остальное — своя страница 404, а не стандартный текст
	mux.HandleFunc("/", s.notFound)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })

	return s.recoverer(securityHeaders(s.auth.Middleware(s.cfg.Secure)(mux)))
}

// view — данные, общие для всех страниц.
type view struct {
	User  *auth.User
	CSRF  string
	Flash string
	// Unread — непрочитанные личные сообщения (счётчик в шапке).
	Unread int
	// AskVerify — показать плашку «подтвердите почту».
	AskVerify bool
	Data   any
}

// render рисует страницу в буфер, чтобы ошибка шаблона не оставила
// полуотправленный ответ.
func (s *Server) render(w http.ResponseWriter, r *http.Request, status int, page string, data any) {
	t, ok := s.pages[page]
	if !ok {
		s.serverError(w, r, fmt.Errorf("нет шаблона %s", page))
		return
	}
	v := view{
		User: auth.CurrentUser(r.Context()),
		CSRF: auth.CSRFToken(r.Context()),
		Data: data,
	}
	v.AskVerify = v.User != nil && !v.User.Verified && s.cfg.RequireVerification
	if v.User != nil && s.dm != nil {
		// без счётчика страница всё равно нужна: ошибку только пишем в лог
		n, err := s.dm.Unread(r.Context(), v.User.ID)
		if err != nil {
			s.log.Warn("счётчик сообщений", "err", err)
		}
		v.Unread = n
	}
	if c, err := r.Cookie("tropa_flash"); err == nil {
		v.Flash = flashText[c.Value]
		http.SetCookie(w, &http.Cookie{Name: "tropa_flash", Path: "/", MaxAge: -1})
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, v); err != nil {
		s.serverError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	buf.WriteTo(w)
}

// flash оставляет короткое сообщение для следующей страницы.
// Только ASCII-безопасный ключ: значение — код сообщения, не текст.
func flash(w http.ResponseWriter, code string) {
	http.SetCookie(w, &http.Cookie{Name: "tropa_flash", Value: code, Path: "/", MaxAge: 60, HttpOnly: true, SameSite: http.SameSiteLaxMode})
}

func (s *Server) serverError(w http.ResponseWriter, r *http.Request, err error) {
	s.log.Error("ошибка обработки запроса", "method", r.Method, "path", r.URL.Path, "err", err)
	http.Error(w, "Что-то пошло не так. Попробуйте ещё раз.", http.StatusInternalServerError)
}

func (s *Server) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				if v == http.ErrAbortHandler {
					panic(v)
				}
				s.serverError(w, r, fmt.Errorf("panic: %v", v))
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		// Плитки OSM требуют Referer: отдаём только origin, без пути и параметров.
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data: https://tile.openstreetmap.org; "+
			"style-src 'self'; script-src 'self'; connect-src 'self'; form-action 'self'; frame-ancestors 'none'")
		next.ServeHTTP(w, r)
	})
}
