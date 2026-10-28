package web

import (
	"errors"
	"net/http"

	"tropa/internal/auth"
	"tropa/internal/meetups"
	"tropa/internal/places"
)

type meetupListView struct {
	Filter     meetups.Filter
	Page       meetups.Page
	Categories []places.Category
}

func categoryNames() []string {
	out := make([]string, len(places.Categories))
	for i, c := range places.Categories {
		out[i] = string(c)
	}
	return out
}

func (s *Server) meetupList(w http.ResponseWriter, r *http.Request) {
	f := meetups.ParseFilter(r.URL.Query(), categoryNames())
	page, err := s.meetups.List(r.Context(), f)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "meetups.html", meetupListView{Filter: f, Page: page, Categories: places.Categories})
}

type meetupFormView struct {
	Place       *places.Place
	Meetup      *meetups.Meetup // nil — новая встреча
	Input       meetups.Input
	Errors      map[string]string
	Visibility  []meetups.Visibility
	Today       string
	MaxCapacity int
}

func (s *Server) meetupForm(w http.ResponseWriter, r *http.Request, status int, v meetupFormView) {
	v.Visibility = meetups.Visibilities
	v.Today = s.meetups.Now().In(meetups.Almaty).Format(meetups.DateLayout)
	v.MaxCapacity = meetups.MaxCapacity
	s.render(w, r, status, "meetup_form.html", v)
}

func meetupInput(r *http.Request) meetups.Input {
	return meetups.Input{
		Title:       r.PostFormValue("title"),
		Description: r.PostFormValue("description"),
		Date:        r.PostFormValue("date"),
		Time:        r.PostFormValue("time"),
		Capacity:    r.PostFormValue("capacity"),
		Visibility:  r.PostFormValue("visibility"),
	}
}

// loadPlace — опубликованное место из {slug} или 404.
func (s *Server) loadPlace(w http.ResponseWriter, r *http.Request) (*places.Place, bool) {
	p, err := s.places.Get(r.Context(), r.PathValue("slug"))
	if errors.Is(err, places.ErrNotFound) {
		s.notFound(w, r)
		return nil, false
	}
	if err != nil {
		s.serverError(w, r, err)
		return nil, false
	}
	return p, true
}

func (s *Server) newMeetup(w http.ResponseWriter, r *http.Request) {
	p, ok := s.loadPlace(w, r)
	if !ok {
		return
	}
	s.meetupForm(w, r, http.StatusOK, meetupFormView{Place: p, Input: meetups.Input{
		Title: p.Name, Time: "11:00", Capacity: "8", Visibility: string(meetups.Public),
	}})
}

func (s *Server) createMeetup(w http.ResponseWriter, r *http.Request) {
	p, ok := s.loadPlace(w, r)
	if !ok {
		return
	}
	in := meetupInput(r)
	m, errs, err := s.meetups.Create(r.Context(), auth.CurrentUser(r.Context()), p.ID, in)
	if errors.Is(err, meetups.ErrInvalidInput) {
		s.meetupForm(w, r, http.StatusUnprocessableEntity, meetupFormView{Place: p, Input: in, Errors: errs})
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	flash(w, "meetup_created")
	http.Redirect(w, r, m.URL(), http.StatusSeeOther)
}

// loadMeetup — встреча по токену из ссылки. Неверный токен — 404,
// одинаковый для «нет такой» и «для друзей без ссылки».
func (s *Server) loadMeetup(w http.ResponseWriter, r *http.Request) (*meetups.Meetup, bool) {
	m, err := s.meetups.ByToken(r.Context(), r.PathValue("token"))
	if errors.Is(err, meetups.ErrNotFound) {
		s.notFound(w, r)
		return nil, false
	}
	if err != nil {
		s.serverError(w, r, err)
		return nil, false
	}
	return m, true
}

type meetupView struct {
	Meetup       *meetups.Meetup
	Participants []meetups.Participant
	Me           *meetups.Participant // nil — не записан
	CanManage    bool
	Past         bool
	Editable     bool
	ShareURL     string
}

func (s *Server) showMeetup(w http.ResponseWriter, r *http.Request) {
	m, ok := s.loadMeetup(w, r)
	if !ok {
		return
	}
	u := auth.CurrentUser(r.Context())
	now := s.meetups.Now()
	v := meetupView{Meetup: m, CanManage: m.CanManage(u), Past: m.Past(now), Editable: m.Editable(now)}
	var err error
	if v.Participants, err = s.meetups.Participants(r.Context(), m.ID); err != nil {
		s.serverError(w, r, err)
		return
	}
	if u != nil {
		for i := range v.Participants {
			if v.Participants[i].UserID == u.ID {
				v.Me = &v.Participants[i]
			}
		}
	}
	if v.CanManage {
		scheme := "http"
		if s.cfg.Secure {
			scheme = "https"
		}
		v.ShareURL = scheme + "://" + r.Host + m.URL()
	}
	// ссылка — ключ к встрече «для друзей»: не отдаём её в Referer сторонним сайтам
	w.Header().Set("Referrer-Policy", "same-origin")
	s.render(w, r, http.StatusOK, "meetup.html", v)
}

// loadManagedMeetup — loadMeetup плюс право на управление (иначе 403).
func (s *Server) loadManagedMeetup(w http.ResponseWriter, r *http.Request) (*meetups.Meetup, bool) {
	m, ok := s.loadMeetup(w, r)
	if !ok {
		return nil, false
	}
	if !m.CanManage(auth.CurrentUser(r.Context())) {
		http.Error(w, "Менять встречу может только организатор", http.StatusForbidden)
		return nil, false
	}
	return m, true
}

func (s *Server) editMeetup(w http.ResponseWriter, r *http.Request) {
	m, ok := s.loadManagedMeetup(w, r)
	if !ok {
		return
	}
	if !m.Editable(s.meetups.Now()) {
		flash(w, "meetup_locked")
		http.Redirect(w, r, m.URL(), http.StatusSeeOther)
		return
	}
	p, err := s.places.Get(r.Context(), m.PlaceSlug)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.meetupForm(w, r, http.StatusOK, meetupFormView{Place: p, Meetup: m, Input: meetups.FromMeetup(m)})
}

func (s *Server) updateMeetup(w http.ResponseWriter, r *http.Request) {
	m, ok := s.loadManagedMeetup(w, r)
	if !ok {
		return
	}
	in := meetupInput(r)
	errs, err := s.meetups.Update(r.Context(), auth.CurrentUser(r.Context()), m.ID, in)
	switch {
	case errors.Is(err, meetups.ErrInvalidInput):
		p, perr := s.places.Get(r.Context(), m.PlaceSlug)
		if perr != nil {
			s.serverError(w, r, perr)
			return
		}
		s.meetupForm(w, r, http.StatusUnprocessableEntity, meetupFormView{Place: p, Meetup: m, Input: in, Errors: errs})
		return
	case errors.Is(err, meetups.ErrNotEditable):
		flash(w, "meetup_locked")
	case err != nil:
		s.serverError(w, r, err)
		return
	default:
		flash(w, "meetup_saved")
	}
	http.Redirect(w, r, m.URL(), http.StatusSeeOther)
}

func (s *Server) setMeetupStatus(w http.ResponseWriter, r *http.Request) {
	m, ok := s.loadManagedMeetup(w, r)
	if !ok {
		return
	}
	to := meetups.Status(r.PostFormValue("to"))
	err := s.meetups.SetStatus(r.Context(), auth.CurrentUser(r.Context()), m.ID, meetups.Status(r.PostFormValue("from")), to)
	switch {
	case errors.Is(err, meetups.ErrStaleStatus):
		flash(w, "meetup_stale")
	case errors.Is(err, meetups.ErrNotEditable):
		flash(w, "meetup_locked")
	case errors.Is(err, meetups.ErrBadStatus):
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	case err != nil:
		s.serverError(w, r, err)
		return
	default:
		flash(w, "meetup_"+string(to))
	}
	http.Redirect(w, r, m.URL(), http.StatusSeeOther)
}

func (s *Server) joinMeetup(w http.ResponseWriter, r *http.Request) {
	m, ok := s.loadMeetup(w, r)
	if !ok {
		return
	}
	st, err := s.meetups.Join(r.Context(), auth.CurrentUser(r.Context()), m.ID)
	switch {
	case errors.Is(err, meetups.ErrClosed):
		flash(w, "join_closed")
	case err != nil:
		s.serverError(w, r, err)
		return
	default:
		flash(w, "join_"+string(st))
	}
	http.Redirect(w, r, m.URL(), http.StatusSeeOther)
}

func (s *Server) leaveMeetup(w http.ResponseWriter, r *http.Request) {
	m, ok := s.loadMeetup(w, r)
	if !ok {
		return
	}
	err := s.meetups.Leave(r.Context(), auth.CurrentUser(r.Context()), m.ID)
	switch {
	case errors.Is(err, meetups.ErrOrganizer):
		flash(w, "leave_organizer")
	case errors.Is(err, meetups.ErrNotJoined):
	case err != nil:
		s.serverError(w, r, err)
		return
	default:
		flash(w, "left")
	}
	http.Redirect(w, r, m.URL(), http.StatusSeeOther)
}

func (s *Server) acceptOffer(w http.ResponseWriter, r *http.Request) {
	m, ok := s.loadMeetup(w, r)
	if !ok {
		return
	}
	err := s.meetups.Accept(r.Context(), auth.CurrentUser(r.Context()), m.ID)
	switch {
	case errors.Is(err, meetups.ErrNoOffer):
		flash(w, "offer_gone")
	case err != nil:
		s.serverError(w, r, err)
		return
	default:
		flash(w, "join_confirmed")
	}
	http.Redirect(w, r, m.URL(), http.StatusSeeOther)
}

func (s *Server) notFound(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, http.StatusNotFound, "notfound.html", nil)
}
