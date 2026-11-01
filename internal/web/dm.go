package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"tropa/internal/auth"
	"tropa/internal/dm"
	"tropa/internal/friends"
)

// inbox — список переписок: все друзья, недавние сверху, с непрочитанными.
func (s *Server) inbox(w http.ResponseWriter, r *http.Request) {
	threads, err := s.dm.Threads(r.Context(), auth.CurrentUser(r.Context()).ID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "messages.html", threads)
}

type conversationView struct {
	Other    *auth.User
	Messages []dm.Message
	MaxChat  int
}

// friendFromPath — собеседник из /messages/{id}. Не друг — страница
// «нет такой» (сообщения только между друзьями); ok = false — ответ уже отправлен.
func (s *Server) friendFromPath(w http.ResponseWriter, r *http.Request) (*auth.User, bool) {
	u := auth.CurrentUser(r.Context())
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 || id == u.ID {
		s.notFound(w, r)
		return nil, false
	}
	st, err := s.friends.Status(r.Context(), u.ID, id)
	if err != nil {
		s.serverError(w, r, err)
		return nil, false
	}
	if st != friends.Friends {
		s.notFound(w, r)
		return nil, false
	}
	other, err := s.auth.UserByID(r.Context(), id)
	if errors.Is(err, auth.ErrNoUser) {
		s.notFound(w, r)
		return nil, false
	}
	if err != nil {
		s.serverError(w, r, err)
		return nil, false
	}
	return other, true
}

// conversation — переписка с другом; открытие отмечает входящие прочитанными.
func (s *Server) conversation(w http.ResponseWriter, r *http.Request) {
	other, ok := s.friendFromPath(w, r)
	if !ok {
		return
	}
	me := auth.CurrentUser(r.Context())
	msgs, err := s.dm.Conversation(r.Context(), me.ID, other.ID, 0)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if err := s.dm.MarkRead(r.Context(), me.ID, other.ID); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "conversation.html", conversationView{Other: other, Messages: msgs, MaxChat: dm.MaxMessage})
}

// postDirect — отправка личного сообщения: из формы (редирект обратно) или
// из chat.js (Accept: application/json — 204, сообщение придёт по потоку).
func (s *Server) postDirect(w http.ResponseWriter, r *http.Request) {
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
	to, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || to <= 0 {
		s.notFound(w, r)
		return
	}
	if ok, retry := s.limits.chat.allow(strconv.FormatInt(u.ID, 10)); !ok {
		fail(http.StatusTooManyRequests, tooMany(w, retry))
		return
	}
	msg, err := s.dm.Post(r.Context(), u, to, r.PostFormValue("body"))
	switch {
	case errors.Is(err, dm.ErrNotFriends):
		fail(http.StatusForbidden, err.Error())
		return
	case errors.Is(err, dm.ErrBadMessage):
		fail(http.StatusUnprocessableEntity, err.Error())
		return
	case err != nil:
		s.serverError(w, r, err)
		return
	}
	s.dms.publish(dm.Key(msg.FromID, msg.ToID), msg)
	if js {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.Redirect(w, r, "/messages/"+strconv.FormatInt(to, 10)+"#chat", http.StatusSeeOther)
}

// directStream — новые сообщения переписки по SSE. Входящие, пришедшие в
// открытый поток, сразу считаются прочитанными.
func (s *Server) directStream(w http.ResponseWriter, r *http.Request) {
	other, ok := s.friendFromPath(w, r)
	if !ok {
		return
	}
	me := auth.CurrentUser(r.Context())
	after := lastEventID(r)
	ch, cancel := s.dms.subscribe(dm.Key(me.ID, other.ID))
	defer cancel()
	missed, err := s.dm.Conversation(r.Context(), me.ID, other.ID, max(after, 0))
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	serveEvents(w, r, after, missed, ch, func(m dm.Message) (int64, any) {
		if m.ToID == me.ID {
			if err := s.dm.MarkRead(r.Context(), me.ID, other.ID); err != nil && r.Context().Err() == nil {
				s.log.Warn("отметка прочитанного", "err", err)
			}
		}
		return m.ID, chatEvent{ID: m.ID, UserID: m.FromID, Name: m.Name, Body: m.Body, At: clock(m.CreatedAt)}
	})
}
