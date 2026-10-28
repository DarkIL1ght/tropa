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
	"time"

	"tropa/internal/auth"
	"tropa/internal/places"
	"tropa/internal/trips"
	"tropa/internal/weather"
)

//go:embed templates static
var assets embed.FS

type Config struct {
	// Secure — cookie только по HTTPS (включать за прокси с TLS).
	Secure bool
}

// Services — бизнес-сервисы, которыми пользуются обработчики.
type Services struct {
	Auth   *auth.Service
	Trips  *trips.Service
	Places *places.Service
	// Weather — погода для плашки на карте; nil — без погоды.
	Weather *weather.Client
}

type Server struct {
	cfg     Config
	auth    *auth.Service
	trips   *trips.Service
	places  *places.Service
	weather *weather.Client
	// now — текущее время (подменяется в тестах): от него зависит «не сезон».
	now   func() time.Time
	log   *slog.Logger
	pages map[string]*template.Template
}

func New(cfg Config, svc Services, log *slog.Logger) (*Server, error) {
	s := &Server{cfg: cfg, auth: svc.Auth, trips: svc.Trips, places: svc.Places, weather: svc.Weather, now: time.Now, log: log}
	if err := s.loadTemplates(); err != nil {
		return nil, err
	}
	return s, nil
}

var funcs = template.FuncMap{
	"date":      func(t time.Time) string { return t.Format("02.01.2006") },
	"dateRange": dateRange,
	"money":     money,
	"plural":    plural,
	"add":       func(a, b int) int { return a + b },
	"sub":       func(a, b int) int { return a - b },
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
	mux.HandleFunc("GET /register", s.registerForm)
	mux.HandleFunc("POST /register", s.register)
	mux.HandleFunc("GET /login", s.loginForm)
	mux.HandleFunc("POST /login", s.login)
	mux.HandleFunc("POST /logout", s.logout)
	mux.Handle("GET /me", auth.RequireUser(http.HandlerFunc(s.me)))

	mux.HandleFunc("GET /trips", s.catalog)
	mux.Handle("GET /trips/new", auth.RequireUser(http.HandlerFunc(s.newTrip)))
	mux.Handle("POST /trips", auth.RequireUser(http.HandlerFunc(s.createTrip)))
	mux.HandleFunc("GET /trips/{id}", s.showTrip)
	mux.Handle("GET /trips/{id}/edit", auth.RequireUser(http.HandlerFunc(s.editTrip)))
	mux.Handle("POST /trips/{id}/edit", auth.RequireUser(http.HandlerFunc(s.updateTrip)))
	mux.Handle("POST /trips/{id}/status", auth.RequireUser(http.HandlerFunc(s.setTripStatus)))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })

	return s.recoverer(securityHeaders(s.auth.Middleware(s.cfg.Secure)(mux)))
}

// view — данные, общие для всех страниц.
type view struct {
	User  *auth.User
	CSRF  string
	Flash string
	Data  any
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
