package web

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"tropa/internal/auth"
	"tropa/internal/places"
)

// «Предложить место» и модерация мест в админке.

// mineLimit — сколько своих предложений видно под формой.
const mineLimit = 30

// adminPlacesLimit — сколько мест в очереди и в истории модерации.
const adminPlacesLimit = 100

var seasons = []places.Season{"all_year", "summer", "winter"}

// kindGroup — группа мест и её виды: optgroup в выпадающем списке.
type kindGroup struct {
	Category places.Category
	Kinds    []places.Kind
}

func kindGroups() []kindGroup {
	out := make([]kindGroup, len(places.Categories))
	for i, c := range places.Categories {
		out[i] = kindGroup{Category: c, Kinds: places.Kinds(c)}
	}
	return out
}

// placeInput — поля формы как ввёл человек. Вид — «группа:вид»
// (nature:lake), координаты — «широта, долгота»: так их копируют из карт.
type placeInput struct {
	Name, Kind, Coords, Address, Description, Website, Season string
	Indoor, Free                                              bool
}

func placeInputFrom(r *http.Request) placeInput {
	return placeInput{
		Name:        r.PostFormValue("name"),
		Kind:        r.PostFormValue("kind"),
		Coords:      r.PostFormValue("coords"),
		Address:     r.PostFormValue("address"),
		Description: r.PostFormValue("description"),
		Website:     r.PostFormValue("website"),
		Season:      r.PostFormValue("season"),
		Indoor:      r.PostFormValue("indoor") == "1",
		Free:        r.PostFormValue("free") == "1",
	}
}

func inputOf(p *places.Place) placeInput {
	return placeInput{
		Name: p.Name, Kind: string(p.Category) + ":" + string(p.Kind),
		Coords:  strconv.FormatFloat(p.Lat, 'f', 6, 64) + ", " + strconv.FormatFloat(p.Lon, 'f', 6, 64),
		Address: p.Address, Description: p.Description, Website: p.Website, Season: string(p.Season),
		Indoor: p.Indoor, Free: p.Free,
	}
}

// parseCoords — «43.2389, 76.9455»; ok=false — не похоже на точку.
func parseCoords(s string) (lat, lon float64, ok bool) {
	parts := strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ';' || r == ' ' })
	if len(parts) != 2 {
		return 0, 0, false
	}
	lat, err1 := strconv.ParseFloat(parts[0], 64)
	lon, err2 := strconv.ParseFloat(parts[1], 64)
	return lat, lon, err1 == nil && err2 == nil
}

// place — место из формы. Неверные координаты дают нулевую точку: её
// отсеет проверка области с понятной ошибкой.
func (in placeInput) place() *places.Place {
	cat, kind, _ := strings.Cut(in.Kind, ":")
	lat, lon, _ := parseCoords(in.Coords)
	return &places.Place{
		Name: in.Name, Category: places.Category(cat), Kind: places.Kind(kind), Lat: lat, Lon: lon,
		Address: in.Address, Description: in.Description, Website: in.Website,
		Season: places.Season(in.Season), Indoor: in.Indoor, Free: in.Free,
	}
}

type placeFormView struct {
	Input   placeInput
	Errors  map[string]string
	Groups  []kindGroup
	Seasons []places.Season
	// Draft — правка черновика админом; nil — форма «Предложить место».
	Draft *places.Suggested
	Mine  []*places.Suggested
	Max   int
	Error string
}

func (s *Server) placeForm(w http.ResponseWriter, r *http.Request, status int, v placeFormView) {
	v.Groups, v.Seasons, v.Max = kindGroups(), seasons, places.MaxPending
	if v.Input.Season == "" {
		v.Input.Season = "all_year"
	}
	if v.Draft == nil {
		var err error
		if v.Mine, err = s.places.ByAuthor(r.Context(), auth.CurrentUser(r.Context()).ID, mineLimit); err != nil {
			s.serverError(w, r, err)
			return
		}
	}
	s.render(w, r, status, "place_form.html", v)
}

func (s *Server) suggestForm(w http.ResponseWriter, r *http.Request) {
	s.placeForm(w, r, http.StatusOK, placeFormView{})
}

func (s *Server) suggestPlace(w http.ResponseWriter, r *http.Request) {
	u := auth.CurrentUser(r.Context())
	in := placeInputFrom(r)
	if ok, retry := s.limits.suggest.allow(strconv.FormatInt(u.ID, 10)); !ok {
		s.placeForm(w, r, http.StatusTooManyRequests, placeFormView{Input: in, Error: tooMany(w, retry)})
		return
	}
	errs, err := s.places.Suggest(r.Context(), u.ID, u.Name, in.place())
	switch {
	case errors.Is(err, places.ErrInvalid):
		s.placeForm(w, r, http.StatusUnprocessableEntity, placeFormView{Input: in, Errors: errs})
		return
	case errors.Is(err, places.ErrTooManyPending):
		s.placeForm(w, r, http.StatusConflict, placeFormView{Input: in,
			Error: fmt.Sprintf("У вас уже %d мест на проверке. Дождитесь, пока мы их посмотрим.", places.MaxPending)})
		return
	case err != nil:
		s.serverError(w, r, err)
		return
	}
	flash(w, "place_suggested")
	http.Redirect(w, r, "/places/suggest", http.StatusSeeOther)
}

type adminPlacesView struct {
	Pending  []*places.Suggested
	Reviewed []*places.Suggested
	Limit    int
}

func (s *Server) adminPlaces(w http.ResponseWriter, r *http.Request) {
	v := adminPlacesView{Limit: adminPlacesLimit}
	var err error
	if v.Pending, err = s.places.Pending(r.Context(), adminPlacesLimit); err != nil {
		s.serverError(w, r, err)
		return
	}
	if v.Reviewed, err = s.places.Reviewed(r.Context(), 20); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "admin_places.html", v)
}

func placeID(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return id, err == nil && id > 0
}

// adminPlaceEdit — черновик в форме для правки перед публикацией.
func (s *Server) adminPlaceEdit(w http.ResponseWriter, r *http.Request) {
	id, ok := placeID(r)
	if !ok {
		s.notFound(w, r)
		return
	}
	p, err := s.places.PendingByID(r.Context(), id)
	if errors.Is(err, places.ErrNotFound) {
		flash(w, "place_reviewed")
		http.Redirect(w, r, "/admin/places", http.StatusSeeOther)
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.placeForm(w, r, http.StatusOK, placeFormView{Input: inputOf(&p.Place), Draft: p})
}

// adminPublish публикует черновик: из очереди — как есть, из формы
// правки (edit=1) — с полями формы.
func (s *Server) adminPublish(w http.ResponseWriter, r *http.Request) {
	id, ok := placeID(r)
	if !ok {
		s.notFound(w, r)
		return
	}
	var edit *places.Place
	in := placeInputFrom(r)
	if r.PostFormValue("edit") == "1" {
		edit = in.place()
	}
	errs, err := s.places.Publish(r.Context(), id, edit)
	switch {
	case errors.Is(err, places.ErrInvalid):
		p, err := s.places.PendingByID(r.Context(), id)
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		s.placeForm(w, r, http.StatusUnprocessableEntity, placeFormView{Input: in, Errors: errs, Draft: p})
		return
	case errors.Is(err, places.ErrNotFound), errors.Is(err, places.ErrReviewed):
		flash(w, "place_reviewed")
	case err != nil:
		s.serverError(w, r, err)
		return
	default:
		flash(w, "place_published")
	}
	http.Redirect(w, r, "/admin/places", http.StatusSeeOther)
}

func (s *Server) adminReject(w http.ResponseWriter, r *http.Request) {
	id, ok := placeID(r)
	if !ok {
		s.notFound(w, r)
		return
	}
	err := s.places.Reject(r.Context(), id, r.PostFormValue("reason"))
	switch {
	case errors.Is(err, places.ErrInvalid):
		flash(w, "reason_required")
	case errors.Is(err, places.ErrNotFound), errors.Is(err, places.ErrReviewed):
		flash(w, "place_reviewed")
	case err != nil:
		s.serverError(w, r, err)
		return
	default:
		flash(w, "place_rejected")
	}
	http.Redirect(w, r, "/admin/places", http.StatusSeeOther)
}
