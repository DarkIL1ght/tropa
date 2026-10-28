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

// hub раздаёт новые сообщения открытым потокам чата (SSE) в этом процессе.
// Сервер работает в одном экземпляре, поэтому общей шины не нужно; при
// нескольких экземплярах — LISTEN/NOTIFY в PostgreSQL.
type hub struct {
	mu     sync.Mutex
	subs   map[int64]map[chan meetups.Message]struct{}
	closed bool
}

func newHub() *hub { return &hub{subs: map[int64]map[chan meetups.Message]struct{}{}} }

// subscribe — канал новых сообщений встречи. Канал закрывается, если
// подписчик не успевает читать (клиент переподключится и догрузит
// пропущенное по Last-Event-ID) или сервер останавливается.
func (h *hub) subscribe(meetupID int64) (chan meetups.Message, func()) {
	ch := make(chan meetups.Message, 16)
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		close(ch)
		return ch, func() {}
	}
	if h.subs[meetupID] == nil {
		h.subs[meetupID] = map[chan meetups.Message]struct{}{}
	}
	h.subs[meetupID][ch] = struct{}{}
	return ch, func() { h.drop(meetupID, ch) }
}

func (h *hub) drop(meetupID int64, ch chan meetups.Message) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.subs[meetupID][ch]; ok {
		delete(h.subs[meetupID], ch)
		close(ch)
		if len(h.subs[meetupID]) == 0 {
			delete(h.subs, meetupID)
		}
	}
}

func (h *hub) publish(msg meetups.Message) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs[msg.MeetupID] {
		select {
		case ch <- msg:
		default: // медленный клиент: отключаем, он догрузит сообщения из базы
			delete(h.subs[msg.MeetupID], ch)
			close(ch)
		}
	}
}

// close завершает все потоки — при остановке сервера, иначе Shutdown ждал бы их.
func (h *hub) close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closed = true
	for id, set := range h.subs {
		for ch := range set {
			close(ch)
		}
		delete(h.subs, id)
	}
}

// Shutdown закрывает открытые потоки чата; вызывать при остановке HTTP-сервера.
func (s *Server) Shutdown() { s.chat.close() }

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
	s.chat.publish(msg)
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
	after, _ := strconv.ParseInt(r.Header.Get("Last-Event-ID"), 10, 64)
	if after == 0 {
		after, _ = strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	}

	ch, cancel := s.chat.subscribe(m.ID)
	defer cancel()
	// after = 0 — на странице не было сообщений: шлём последние, клиент
	// пропускает уже показанные по id
	missed, err := s.meetups.Messages(r.Context(), m.ID, max(after, 0))
	if err != nil {
		s.serverError(w, r, err)
		return
	}

	rc := http.NewResponseController(w)
	rc.SetWriteDeadline(time.Time{}) // общий WriteTimeout сервера не для долгого потока
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	send := func(msg meetups.Message) error {
		if msg.ID <= after {
			return nil
		}
		after = msg.ID
		b, _ := json.Marshal(newChatEvent(msg))
		if _, err := fmt.Fprintf(w, "id: %d\ndata: %s\n\n", msg.ID, b); err != nil {
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
