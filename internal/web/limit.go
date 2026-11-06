package web

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// maxEmailBytes — предел длины адреса по RFC 5321: адрес длиннее не проверяется
	// и ключом ведра не становится.
	maxEmailBytes = 254
	// maxAuthForm — предел тела форм входа и сброса пароля.
	maxAuthForm = 64 << 10
)

// limiter — «ведро жетонов» на ключ (IP, IP и email, пользователь): до burst
// попыток подряд, затем по одной раз в per. Хранится в памяти: сервер
// запускается в одном экземпляре (см. long polling бота), после рестарта
// счётчики обнуляются — для защиты от подбора этого достаточно.
type limiter struct {
	burst float64
	per   time.Duration
	now   func() time.Time

	mu        sync.Mutex
	buckets   map[string]*bucket
	lastSweep time.Time
}

type bucket struct {
	tokens float64
	at     time.Time
}

func newLimiter(burst int, per time.Duration) *limiter {
	return &limiter{burst: float64(burst), per: per, now: time.Now, buckets: map[string]*bucket{}}
}

// allow тратит попытку по ключу. Если попыток нет — false и через сколько
// появится следующая.
func (l *limiter) allow(key string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	full := time.Duration(l.burst * float64(l.per))
	// полные вёдра ничем не отличаются от отсутствующих — выбрасываем их
	if now.Sub(l.lastSweep) > full {
		for k, b := range l.buckets {
			if now.Sub(b.at) >= full {
				delete(l.buckets, k)
			}
		}
		l.lastSweep = now
	}
	b, ok := l.buckets[key]
	if !ok {
		b = &bucket{tokens: l.burst, at: now}
		l.buckets[key] = b
	}
	b.tokens = min(l.burst, b.tokens+float64(now.Sub(b.at))/float64(l.per))
	b.at = now
	if b.tokens < 1 {
		return false, time.Duration((1 - b.tokens) * float64(l.per))
	}
	b.tokens--
	return true, 0
}

// limits — ограничения частоты для форм, которые можно перебирать.
type limits struct {
	loginIP, loginPair   *limiter
	register             *limiter
	password             *limiter
	forgotIP, forgotPair *limiter
	chat                 *limiter
	friend               *limiter
	suggest              *limiter
	verify, resend       *limiter
}

func newLimits() *limits {
	return &limits{
		loginIP:   newLimiter(10, 30*time.Second),
		loginPair: newLimiter(10, 2*time.Minute),
		register:  newLimiter(5, 10*time.Minute),
		password:  newLimiter(5, 3*time.Minute),
		// сброс шлёт сообщения в Telegram — не даём спамить с одного адреса
		forgotIP:   newLimiter(5, 10*time.Minute),
		forgotPair: newLimiter(3, 10*time.Minute),
		// чат: короткая пачка можно, поток — не чаще раза в секунду
		chat: newLimiter(5, time.Second),
		// заявки в друзья шлют уведомления — не даём рассылать их пачками
		friend: newLimiter(10, 2*time.Minute),
		// предложения мест уведомляют админов: 3 подряд, затем одно в 10 минут
		suggest: newLimiter(3, 10*time.Minute),
		// ввод кода (у самого кода ещё свой предел попыток) и повторные письма
		verify: newLimiter(10, time.Minute),
		resend: newLimiter(3, 10*time.Minute),
	}
}

// clientIP — адрес посетителя. За прокси (TrustProxy) — последний адрес из
// X-Forwarded-For: его дописал наш прокси, всё левее мог подставить клиент.
func (s *Server) clientIP(r *http.Request) string {
	if s.cfg.TrustProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			if ip := strings.TrimSpace(parts[len(parts)-1]); ip != "" {
				return ip
			}
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// pairKey — ключ ведра пары «адрес + email» без учёта регистра. Длина адреса
// в начале не даёт разным парам склеиться: в email может быть любой символ.
func pairKey(ip, email string) string {
	return strconv.Itoa(len(ip)) + ":" + ip + strings.ToLower(email)
}

// allowPair тратит попытку адреса, затем пары. С исчерпанного адреса новые
// вёдра пар не создаются: иначе перебор адресов почты занимал бы память.
func allowPair(byIP, byPair *limiter, ip, email string) (bool, time.Duration) {
	if ok, retry := byIP.allow(ip); !ok {
		return false, retry
	}
	return byPair.allow(pairKey(ip, email))
}

// limitAuthForms ограничивает тело форм входа и сброса пароля. Стоит до
// auth.Middleware: та разбирает форму первой, ради CSRF-токена.
func limitAuthForms(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && (r.URL.Path == "/login" || r.URL.Path == "/password/forgot") {
			r.Body = http.MaxBytesReader(w, r.Body, maxAuthForm)
			var tooLarge *http.MaxBytesError
			if err := r.ParseForm(); errors.As(err, &tooLarge) {
				http.Error(w, "Слишком большой запрос", http.StatusRequestEntityTooLarge)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// tooMany — текст ошибки и заголовок Retry-After для ответа 429.
func tooMany(w http.ResponseWriter, retry time.Duration) string {
	sec := int(retry.Seconds()) + 1
	w.Header().Set("Retry-After", fmt.Sprint(sec))
	if sec < 60 {
		return "Слишком много попыток. Попробуйте через минуту."
	}
	return fmt.Sprintf("Слишком много попыток. Попробуйте через %d %s.", (sec+59)/60, plural((sec+59)/60, "минуту", "минуты", "минут"))
}
