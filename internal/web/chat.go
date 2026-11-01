package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"tropa/internal/auth"
	"tropa/internal/meetups"
)

// hub раздаёт новые сообщения открытым потокам (SSE) в этом процессе: чату
// встречи (ключ — встреча) и личной переписке (ключ — пара). Сервер работает
// в одном экземпляре, поэтому общей шины не нужно; при нескольких
// экземплярах — LISTEN/NOTIFY в PostgreSQL.
type hub[M any] struct {
	mu     sync.Mutex
	subs   map[string]map[chan M]struct{}
	closed bool
}

func newHub[M any]() *hub[M] { return &hub[M]{subs: map[string]map[chan M]struct{}{}} }

// subscribe — канал новых сообщений по ключу. Канал закрывается, если
// подписчик не успевает читать (клиент переподключится и догрузит
// пропущенное по Last-Event-ID) или сервер останавливается.
func (h *hub[M]) subscribe(key string) (chan M, func()) {
	ch := make(chan M, 16)
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		close(ch)
		return ch, func() {}
	}
	if h.subs[key] == nil {
		h.subs[key] = map[chan M]struct{}{}
	}
	h.subs[key][ch] = struct{}{}
	return ch, func() { h.drop(key, ch) }
}

func (h *hub[M]) drop(key string, ch chan M) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.subs[key][ch]; ok {
		delete(h.subs[key], ch)
		close(ch)
		if len(h.subs[key]) == 0 {
			delete(h.subs, key)
		}
	}
}

func (h *hub[M]) publish(key string, msg M) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs[key] {
		select {
		case ch <- msg:
		default: // медленный клиент: отключаем, он догрузит сообщения из базы
			delete(h.subs[key], ch)
			close(ch)
		}
	}
}

// close завершает все потоки — при остановке сервера, иначе Shutdown ждал бы их.
func (h *hub[M]) close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closed = true
	for key, set := range h.subs {
		for ch := range set {
			close(ch)
		}
		delete(h.subs, key)
	}
}

func meetupKey(id int64) string { return "m:" + strconv.FormatInt(id, 10) }

// Shutdown закрывает открытые потоки чатов; вызывать при остановке HTTP-сервера.
func (s *Server) Shutdown() {
	s.chat.close()
	s.dms.close()
}

// chatAccess — участник, которому доступен чат встречи, и может ли он
// писать (админ без участия только читает — для модерации).
func (s *Server) chatAccess(r *http.Request, m *meetups.Meetup) (read, write bool, err error) {
	u := auth.CurrentUser(r.Context())
	if u == nil {
		return false, false, nil
	}
	p, err := s.meetups.Participant(r.Context(), m.ID, u.ID)
	if errors.Is(err, meetups.ErrNotJoined) {
		p, err = nil, nil
	}
	if err != nil {
		return false, false, err
	}
	write = meetups.CanChat(p)
	return write || u.IsAdmin(), write, nil
}

// postMessage — отправка сообщения: из формы (редирект обратно к чату)
// или из chat.js (Accept: application/json — 204, сообщение придёт по потоку).
func (s *Server) postMessage(w http.ResponseWriter, r *http.Request) {
	m, ok := s.loadMeetup(w, r)
	if !ok {
		return
	}
	u := auth.CurrentUser(r.Context())
	js := strings.Contains(r.Header.Get("Accept"), "application/json")
	fail := func(status int, text string) {
		if js {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			json.NewEncoder(w).Encode(map[string]string{"error": text})
			return
		}
		http.Error(w, text, status)
	}
	if ok, retry := s.limits.chat.allow(strconv.FormatInt(u.ID, 10)); !ok {
		fail(http.StatusTooManyRequests, tooMany(w, retry))
		return
	}
	msg, err := s.meetups.Post(r.Context(), m, u, r.PostFormValue("body"))
	switch {
	case errors.Is(err, meetups.ErrNoChat):
		fail(http.StatusForbidden, err.Error())
		return
	case errors.Is(err, meetups.ErrBadMessage):
		fail(http.StatusUnprocessableEntity, err.Error())
		return
	case err != nil:
		s.serverError(w, r, err)
		return
	}
	s.chat.publish(meetupKey(msg.MeetupID), msg)
	if js {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.Redirect(w, r, m.URL()+"#chat", http.StatusSeeOther)
}

// chatEvent — сообщение в потоке SSE. Время — как на странице (Clock, по Алматы), текст
// клиент вставляет только через textContent.
type chatEvent struct {
	ID     int64  `json:"id"`
	UserID int64  `json:"user_id"`
	Name   string `json:"name"`
	Body   string `json:"body"`
	At     string `json:"at"`
}

func newChatEvent(m meetups.Message) chatEvent {
	return chatEvent{ID: m.ID, UserID: m.UserID, Name: m.Name, Body: m.Body, At: meetups.Clock(m.CreatedAt)}
}

const (
	streamPing = 25 * time.Second // комментарий-пинг не даёт прокси закрыть тихое соединение
	streamLife = 30 * time.Minute // потом клиент сам переподключится (EventSource)
)

// stream — новые сообщения встречи по SSE. Сначала подписка, потом
// догрузка из базы после Last-Event-ID — так ничего не теряется между ними.
func (s *Server) stream(w http.ResponseWriter, r *http.Request) {
	m, ok := s.loadMeetup(w, r)
	if !ok {
		return
	}
	read, _, err := s.chatAccess(r, m)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if !read {
		http.Error(w, meetups.ErrNoChat.Error(), http.StatusForbidden)
		return
	}
	after := lastEventID(r)
	ch, cancel := s.chat.subscribe(meetupKey(m.ID))
	defer cancel()
	// after = 0 — на странице не было сообщений: шлём последние, клиент
	// пропускает уже показанные по id
	missed, err := s.meetups.Messages(r.Context(), m.ID, max(after, 0))
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	serveEvents(w, r, after, missed, ch, func(msg meetups.Message) (int64, any) { return msg.ID, newChatEvent(msg) })
}

// lastEventID — после какого сообщения продолжать поток: Last-Event-ID при
// переподключении EventSource или ?after при первом.
func lastEventID(r *http.Request) int64 {
	after, _ := strconv.ParseInt(r.Header.Get("Last-Event-ID"), 10, 64)
	if after == 0 {
		after, _ = strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	}
	return after
}

// serveEvents — поток SSE: сначала пропущенные сообщения из базы, потом новые
// из канала hub. Подписаться нужно до чтения базы — так ничего не теряется
// между ними; повторы отсекаются по id. event возвращает id и тело события.
func serveEvents[M any](w http.ResponseWriter, r *http.Request, after int64, missed []M, ch <-chan M, event func(M) (int64, any)) {
	rc := http.NewResponseController(w)
	rc.SetWriteDeadline(time.Time{}) // общий WriteTimeout сервера не для долгого потока
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	send := func(msg M) error {
		id, data := event(msg)
		if id <= after {
			return nil
		}
		after = id
		b, _ := json.Marshal(data)
		if _, err := fmt.Fprintf(w, "id: %d\ndata: %s\n\n", id, b); err != nil {
			return err
		}
		return rc.Flush()
	}
	for _, msg := range missed {
		if send(msg) != nil {
			return
		}
	}
	fmt.Fprint(w, ": ok\n\n")
	if rc.Flush() != nil {
		return
	}

	ping := time.NewTicker(streamPing)
	defer ping.Stop()
	life := time.NewTimer(streamLife)
	defer life.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-life.C:
			return
		case msg, ok := <-ch:
			if !ok || send(msg) != nil {
				return
			}
		case <-ping.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil || rc.Flush() != nil {
				return
			}
		}
	}
}
