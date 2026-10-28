package web

import (
	"errors"
	"net/http"
	"strconv"

	"tropa/internal/auth"
	"tropa/internal/friends"
	"tropa/internal/meetups"
)

type friendsView struct {
	friends.Lists
	Meetups []*meetups.Meetup
}

// friendsPage — друзья, заявки и предстоящие встречи друзей (в том числе
// «для друзей» — их больше нигде без ссылки не видно).
func (s *Server) friendsPage(w http.ResponseWriter, r *http.Request) {
	u := auth.CurrentUser(r.Context())
	var v friendsView
	var err error
	if v.Lists, err = s.friends.List(r.Context(), u.ID); err != nil {
		s.serverError(w, r, err)
		return
	}
	if v.Meetups, err = s.meetups.ByFriends(r.Context(), u.ID); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "friends.html", v)
}

var friendFlash = map[friends.Status]string{
	friends.Outgoing: "friend_requested",
	friends.Friends:  "friend_added",
}

// friendAction — кнопки дружбы на странице человека и на /friends:
// add (заявка или согласие на встречную), accept, remove (удалить,
// отклонить или отменить). После — назад, откуда пришли.
func (s *Server) friendAction(w http.ResponseWriter, r *http.Request) {
	u := auth.CurrentUser(r.Context())
	other, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || other <= 0 {
		s.notFound(w, r)
		return
	}
	back := auth.SafeNext(r.PostFormValue("next"))
	if back == "/" {
		back = "/u/" + strconv.FormatInt(other, 10)
	}
	code := ""
	switch r.PostFormValue("action") {
	case "add":
		if ok, retry := s.limits.friend.allow(strconv.FormatInt(u.ID, 10)); !ok {
			http.Error(w, tooMany(w, retry), http.StatusTooManyRequests)
			return
		}
		var st friends.Status
		st, err = s.friends.Request(r.Context(), u, other)
		code = friendFlash[st]
	case "accept":
		err = s.friends.Accept(r.Context(), u, other)
		code = "friend_added"
	case "remove":
		err = s.friends.Remove(r.Context(), u.ID, other)
		code = "friend_removed"
	default:
		http.Error(w, "Неизвестное действие", http.StatusBadRequest)
		return
	}
	switch {
	case errors.Is(err, friends.ErrNoUser):
		s.notFound(w, r)
		return
	case errors.Is(err, friends.ErrSelf), errors.Is(err, friends.ErrNoInvite):
		code = "friend_stale"
	case err != nil:
		s.serverError(w, r, err)
		return
	}
	if code != "" {
		flash(w, code)
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}
