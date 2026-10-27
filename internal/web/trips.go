package web

import (
	"errors"
	"net/http"
	"strconv"

	"tropa/internal/auth"
	"tropa/internal/trips"
)

type catalogView struct {
	Filter       trips.Filter
	Page         trips.Page
	Regions      []string
	Difficulties []trips.Difficulty
}

func (s *Server) catalog(w http.ResponseWriter, r *http.Request) {
	f := trips.ParseFilter(r.URL.Query())
	page, err := s.trips.Catalog(r.Context(), f)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	regions, err := s.trips.Regions(r.Context())
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "trips.html", catalogView{
		Filter: f, Page: page, Regions: regions, Difficulties: trips.Difficulties,
	})
}

type tripFormView struct {
	Trip         *trips.Trip // nil — новый поход
	Input        trips.Input
	Errors       map[string]string
	Difficulties []trips.Difficulty
	Today        string
}

func (s *Server) tripForm(w http.ResponseWriter, r *http.Request, status int, v tripFormView) {
	v.Difficulties = trips.Difficulties
	v.Today = s.trips.Today().Format(trips.DateLayout)
	s.render(w, r, status, "trip_form.html", v)
}

func inputFromForm(r *http.Request) trips.Input {
	return trips.Input{
		Title:        r.PostFormValue("title"),
		Description:  r.PostFormValue("description"),
		Region:       r.PostFormValue("region"),
		MeetingPoint: r.PostFormValue("meeting_point"),
		StartDate:    r.PostFormValue("start_date"),
		EndDate:      r.PostFormValue("end_date"),
		Difficulty:   r.PostFormValue("difficulty"),
		Capacity:     r.PostFormValue("capacity"),
		Fee:          r.PostFormValue("fee"),
	}
}

func (s *Server) newTrip(w http.ResponseWriter, r *http.Request) {
	s.tripForm(w, r, http.StatusOK, tripFormView{Input: trips.Input{Difficulty: "easy", Capacity: "10", Fee: "0"}})
}

func (s *Server) createTrip(w http.ResponseWriter, r *http.Request) {
	in := inputFromForm(r)
	id, errs, err := s.trips.Create(r.Context(), auth.CurrentUser(r.Context()), in)
	if errors.Is(err, trips.ErrInvalidInput) {
		s.tripForm(w, r, http.StatusUnprocessableEntity, tripFormView{Input: in, Errors: errs})
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	flash(w, "trip_created")
	http.Redirect(w, r, "/trips/"+strconv.FormatInt(id, 10), http.StatusSeeOther)
}

// loadTrip достаёт поход из {id} и проверяет, что пользователь его видит.
// Невидимый поход отдаётся как 404, чтобы не раскрывать, что он существует.
func (s *Server) loadTrip(w http.ResponseWriter, r *http.Request) (*trips.Trip, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		s.notFound(w, r)
		return nil, false
	}
	t, err := s.trips.Get(r.Context(), id)
	if errors.Is(err, trips.ErrNotFound) || (err == nil && !t.VisibleTo(auth.CurrentUser(r.Context()))) {
		s.notFound(w, r)
		return nil, false
	}
	if err != nil {
		s.serverError(w, r, err)
		return nil, false
	}
	return t, true
}

type tripView struct {
	Trip      *trips.Trip
	CanManage bool
}

func (s *Server) showTrip(w http.ResponseWriter, r *http.Request) {
	t, ok := s.loadTrip(w, r)
	if !ok {
		return
	}
	s.render(w, r, http.StatusOK, "trip.html", tripView{Trip: t, CanManage: t.CanManage(auth.CurrentUser(r.Context()))})
}

// loadManagedTrip — loadTrip плюс право на управление (иначе 403).
func (s *Server) loadManagedTrip(w http.ResponseWriter, r *http.Request) (*trips.Trip, bool) {
	t, ok := s.loadTrip(w, r)
	if !ok {
		return nil, false
	}
	if !t.CanManage(auth.CurrentUser(r.Context())) {
		http.Error(w, "Править поход может только организатор", http.StatusForbidden)
		return nil, false
	}
	return t, true
}

func (s *Server) editTrip(w http.ResponseWriter, r *http.Request) {
	t, ok := s.loadManagedTrip(w, r)
	if !ok {
		return
	}
	if !t.Status.Editable() {
		flash(w, "trip_locked")
		http.Redirect(w, r, "/trips/"+r.PathValue("id"), http.StatusSeeOther)
		return
	}
	s.tripForm(w, r, http.StatusOK, tripFormView{Trip: t, Input: trips.FromTrip(t)})
}

func (s *Server) updateTrip(w http.ResponseWriter, r *http.Request) {
	t, ok := s.loadManagedTrip(w, r)
	if !ok {
		return
	}
	in := inputFromForm(r)
	errs, err := s.trips.Update(r.Context(), auth.CurrentUser(r.Context()), t.ID, in)
	switch {
	case errors.Is(err, trips.ErrInvalidInput):
		s.tripForm(w, r, http.StatusUnprocessableEntity, tripFormView{Trip: t, Input: in, Errors: errs})
		return
	case errors.Is(err, trips.ErrNotEditable):
		flash(w, "trip_locked")
	case err != nil:
		s.serverError(w, r, err)
		return
	default:
		flash(w, "trip_saved")
	}
	http.Redirect(w, r, "/trips/"+r.PathValue("id"), http.StatusSeeOther)
}

func (s *Server) setTripStatus(w http.ResponseWriter, r *http.Request) {
	t, ok := s.loadManagedTrip(w, r)
	if !ok {
		return
	}
	from := trips.Status(r.PostFormValue("from"))
	to := trips.Status(r.PostFormValue("to"))
	err := s.trips.SetStatus(r.Context(), auth.CurrentUser(r.Context()), t.ID, from, to)
	switch {
	case errors.Is(err, trips.ErrStaleStatus):
		flash(w, "trip_stale")
	case errors.Is(err, trips.ErrBadStatus):
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	case err != nil:
		s.serverError(w, r, err)
		return
	default:
		flash(w, "status_"+string(to))
	}
	http.Redirect(w, r, "/trips/"+r.PathValue("id"), http.StatusSeeOther)
}

func (s *Server) notFound(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, http.StatusNotFound, "notfound.html", nil)
}
