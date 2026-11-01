package web

import (
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// limiter — «ведро жетонов» на ключ (IP, email, пользователь): до burst
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
	loginIP, loginEmail   *limiter
	register              *limiter
	password              *limiter
	forgotIP, forgotEmail *limiter
	chat                  *limiter
	friend                *limiter
	suggest               *limiter
	verify, resend        *limiter
}

func newLimits() *limits {
	return &limits{
		loginIP:    newLimiter(10, 30*time.Second),
		loginEmail: newLimiter(10, 2*time.Minute),
		register:   newLimiter(5, 10*time.Minute),
		password:   newLimiter(5, 3*time.Minute),
		// сброс шлёт сообщения в Telegram — не даём спамить чужой аккаунт
		forgotIP:    newLimiter(5, 10*time.Minute),
		forgotEmail: newLimiter(3, 10*time.Minute),
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

// tooMany — текст ошибки и заголовок Retry-After для ответа 429.
func tooMany(w http.ResponseWriter, retry time.Duration) string {
	sec := int(retry.Seconds()) + 1
	w.Header().Set("Retry-After", fmt.Sprint(sec))
	if sec < 60 {
		return "Слишком много попыток. Попробуйте через минуту."
	}
	return fmt.Sprintf("Слишком много попыток. Попробуйте через %d %s.", (sec+59)/60, plural((sec+59)/60, "минуту", "минуты", "минут"))
}
