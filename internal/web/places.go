package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"tropa/internal/meetups"
	"tropa/internal/places"
	"tropa/internal/weather"
)

type mapView struct {
	Categories []places.Category
	// Weather — непогода сейчас: есть повод позвать в тепло. nil — погода
	// хорошая или неизвестна, плашки нет.
	Weather *weather.Weather
}

// mapPage — главная: карта мест во весь экран. Сами места грузит
// map.js через /api/places, чтобы карта обновлялась при сдвиге без перезагрузки.
func (s *Server) mapPage(w http.ResponseWriter, r *http.Request) {
	v := mapView{Categories: places.Categories}
	if s.weather != nil {
		// Ошибку только пишем в лог: без погоды карта работает как обычно.
		if wt, err := s.weather.Current(r.Context()); err != nil {
			s.log.Warn("погода", "err", err)
		} else if wt.Cold() {
			v.Weather = &wt
		}
	}
	s.render(w, r, http.StatusOK, "map.html", v)
}

// parsePlaceQuery читает фильтр карты из query-параметров:
//
//	bbox=south,west,north,east  cat=nature,indoor  indoor=1  free=1  q=текст
//
// Неверные значения — ошибка 400: это API для своего фронтенда, тихо
// игнорировать мусор незачем.
func parsePlaceQuery(v url.Values) (places.Query, error) {
	var q places.Query
	if raw := v.Get("bbox"); raw != "" {
		parts := strings.Split(raw, ",")
		if len(parts) != 4 {
			return q, errors.New("bbox: ждали south,west,north,east")
		}
		var f [4]float64
		for i, p := range parts {
			x, err := strconv.ParseFloat(strings.TrimSpace(p), 64)
			if err != nil {
				return q, errors.New("bbox: не число")
			}
			f[i] = x
		}
		b := places.BBox{South: f[0], West: f[1], North: f[2], East: f[3]}
		if b.South < -90 || b.North > 90 || b.West < -180 || b.East > 180 || b.South > b.North || b.West > b.East {
			return q, errors.New("bbox: координаты вне диапазона")
		}
		q.BBox = &b
	}
	if raw := v.Get("cat"); raw != "" {
		for _, c := range strings.Split(raw, ",") {
			cat := places.Category(c)
			if cat.Label() == "" {
				return q, errors.New("cat: неизвестная группа " + c)
			}
			q.Categories = append(q.Categories, cat)
		}
	}
	q.IndoorOnly = v.Get("indoor") == "1"
	q.FreeOnly = v.Get("free") == "1"
	q.Text = v.Get("q")
	if len([]rune(q.Text)) > 100 {
		return q, errors.New("q: слишком длинный запрос")
	}
	return q, nil
}

// GeoJSON — ровно столько, сколько нужно карте.
type featureCollection struct {
	Type     string    `json:"type"`
	Features []feature `json:"features"`
}

type feature struct {
	Type       string       `json:"type"`
	Geometry   geometry     `json:"geometry"`
	Properties placeSummary `json:"properties"`
}

type geometry struct {
	Type        string     `json:"type"`
	Coordinates [2]float64 `json:"coordinates"` // [lon, lat] — порядок GeoJSON
}

type placeSummary struct {
	Slug      string `json:"slug"`
	Name      string `json:"name"`
	Category  string `json:"category"`
	Kind      string `json:"kind"` // подпись вида, по-русски
	Indoor    bool   `json:"indoor"`
	Free      bool   `json:"free"`
	Season    string `json:"season,omitempty"`     // пусто — круглый год
	OffSeason bool   `json:"off_season,omitempty"` // сейчас не сезон
	Meetups   int    `json:"meetups,omitempty"`    // предстоящих открытых встреч
	URL       string `json:"url"`
}

func (s *Server) apiPlaces(w http.ResponseWriter, r *http.Request) {
	q, err := parsePlaceQuery(r.URL.Query())
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	list, err := s.places.Search(r.Context(), q)
	if err != nil {
		s.log.Error("поиск мест", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "ошибка сервера"})
		return
	}
	ids := make([]int64, len(list))
	for i, p := range list {
		ids[i] = p.ID
	}
	counts, err := s.meetups.CountByPlace(r.Context(), ids)
	if err != nil {
		s.log.Error("встречи на карте", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "ошибка сервера"})
		return
	}
	month := s.month()
	fc := featureCollection{Type: "FeatureCollection", Features: make([]feature, 0, len(list))}
	for _, p := range list {
		season := ""
		if p.Season != "all_year" {
			season = p.Season.Label()
		}
		fc.Features = append(fc.Features, feature{
			Type:     "Feature",
			Geometry: geometry{Type: "Point", Coordinates: [2]float64{p.Lon, p.Lat}},
			Properties: placeSummary{
				Slug: p.Slug, Name: p.Name, Category: string(p.Category), Kind: p.Kind.Label(),
				Indoor: p.Indoor, Free: p.Free, Season: season, OffSeason: p.OffSeason(month),
				Meetups: counts[p.ID], URL: "/places/" + p.Slug,
			},
		})
	}
	w.Header().Set("Cache-Control", "public, max-age=60")
	writeJSON(w, http.StatusOK, fc)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

type placeView struct {
	Place     *places.Place
	OSMURL    string
	OffSeason bool
	Meetups   []*meetups.Meetup
}

func (s *Server) showPlace(w http.ResponseWriter, r *http.Request) {
	p, err := s.places.Get(r.Context(), r.PathValue("slug"))
	if errors.Is(err, places.ErrNotFound) {
		s.notFound(w, r)
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	v := placeView{Place: p, OffSeason: p.OffSeason(s.month())}
	if v.Meetups, err = s.meetups.ByPlace(r.Context(), p.ID); err != nil {
		s.serverError(w, r, err)
		return
	}
	if p.OSMRef != "" {
		v.OSMURL = "https://www.openstreetmap.org/" + p.OSMRef
	}
	s.render(w, r, http.StatusOK, "place.html", v)
}

// month — текущий месяц по Алматы: от него зависит «не сезон».
func (s *Server) month() time.Month { return s.now().In(meetups.Almaty).Month() }
