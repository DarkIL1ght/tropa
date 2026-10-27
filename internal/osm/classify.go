// Package osm — импорт кандидатов в места из OpenStreetMap через Overpass API.
//
// Данные OSM — ODbL: хранить и править можно, подпись «© участники
// OpenStreetMap» обязательна (она есть на карте и странице места).
package osm

import (
	"math"
	"regexp"
	"strings"
	"unicode"

	"tropa/internal/places"
)

// Element — объект из ответа Overpass (`out tags bb`).
type Element struct {
	Type   string            `json:"type"`
	ID     int64             `json:"id"`
	Lat    float64           `json:"lat"` // только у node
	Lon    float64           `json:"lon"`
	Bounds *Bounds           `json:"bounds"` // у way и relation
	Tags   map[string]string `json:"tags"`
}

type Bounds struct {
	MinLat float64 `json:"minlat"`
	MinLon float64 `json:"minlon"`
	MaxLat float64 `json:"maxlat"`
	MaxLon float64 `json:"maxlon"`
}

// Center — точка объекта: координаты узла или центр рамки.
func (e *Element) Center() (lat, lon float64) {
	if e.Bounds != nil {
		return (e.Bounds.MinLat + e.Bounds.MaxLat) / 2, (e.Bounds.MinLon + e.Bounds.MaxLon) / 2
	}
	return e.Lat, e.Lon
}

// SizeMeters — диагональ рамки объекта (0 для узла). Нужна, чтобы
// отсеять крошечные скверы и пешеходные переходы.
func (e *Element) SizeMeters() float64 {
	if e.Bounds == nil {
		return 0
	}
	const r = 6_371_000.0
	lat1, lat2 := e.Bounds.MinLat*math.Pi/180, e.Bounds.MaxLat*math.Pi/180
	dLat := lat2 - lat1
	dLon := (e.Bounds.MaxLon - e.Bounds.MinLon) * math.Pi / 180 * math.Cos((lat1+lat2)/2)
	return r * math.Hypot(dLat, dLon)
}

// Name — русское название, если есть, иначе основное.
func (e *Element) Name() string {
	for _, k := range []string{"name:ru", "name"} {
		if n := strings.TrimSpace(e.Tags[k]); n != "" {
			return n
		}
	}
	return ""
}

// MountainsBBox — горы над Алматы (Иле-Алатау от Каскелена до Тургеня).
// Пики, ущелья и смотровые берём только отсюда: по всей области их тысячи,
// и большинство — не прогулка с друзьями.
var MountainsBBox = places.BBox{South: 42.95, West: 76.55, North: 43.35, East: 77.80}

var (
	anticafeRe   = regexp.MustCompile(`(?i)антикафе|анти-кафе|тайм-?кафе|anti-?cafe|time-?cafe`)
	boardgamesRe = regexp.MustCompile(`(?i)настольн|board ?game|игротек`)
	questRe      = regexp.MustCompile(`(?i)квест|quest|escape`)
	karaokeRe    = regexp.MustCompile(`(?i)караоке|karaoke`)
)

// rule — результат классификации.
type rule struct {
	category places.Category
	kind     places.Kind
	indoor   bool
	free     bool
	season   places.Season
}

// Classify решает, становится ли объект кандидатом и каким. ok=false —
// объект не подходит (нет названия, слишком мал, не та зона и т. п.).
func Classify(e *Element) (r rule, ok bool) {
	t := e.Tags
	name := e.Name()
	if !strings.ContainsFunc(name, unicode.IsLetter) {
		return r, false
	}
	lat, lon := e.Center()
	inCity := places.InCity(lat, lon)
	inMountains := MountainsBBox.Contains(lat, lon)

	switch {
	// --- В тепле: по названию и тегам заведений ---
	case anticafeRe.MatchString(name):
		r = rule{places.Indoor, "anticafe", true, false, ""}
	case boardgamesRe.MatchString(name) && (t["amenity"] != "" || t["shop"] != "" || t["leisure"] != ""):
		r = rule{places.Indoor, "boardgames", true, false, ""}
	case t["leisure"] == "escape_game" || (questRe.MatchString(name) && t["leisure"] != "" && t["leisure"] != "park"):
		r = rule{places.Indoor, "quest", true, false, ""}
	case t["amenity"] == "karaoke_box" || (karaokeRe.MatchString(name) && (t["amenity"] != "" || t["leisure"] != "")):
		r = rule{places.Indoor, "karaoke", true, false, ""}
	case t["amenity"] == "cinema":
		r = rule{places.Indoor, "cinema", true, false, ""}
	case t["shop"] == "mall":
		if e.SizeMeters() < 80 && e.Type != "node" {
			return r, false
		}
		r = rule{places.Indoor, "mall", true, false, ""}

	// --- Активный отдых ---
	case t["leisure"] == "ice_rink":
		indoor := t["indoor"] == "yes" || t["building"] != "" || t["covered"] == "yes"
		season := places.Season("winter")
		if indoor {
			season = ""
		}
		r = rule{places.Active, "rink", indoor, false, season}
	case t["landuse"] == "winter_sports" || t["leisure"] == "ski_resort":
		r = rule{places.Active, "ski", false, false, "winter"}
	case t["sport"] == "climbing" && (t["leisure"] == "sports_centre" || t["leisure"] == "fitness_centre" || t["building"] != ""):
		r = rule{places.Active, "climbing", true, false, ""}
	case t["leisure"] == "bowling_alley" || t["sport"] == "10pin":
		r = rule{places.Active, "bowling", true, false, ""}
	case t["sport"] == "karting" || (t["sport"] == "motor" && strings.Contains(strings.ToLower(name), "карт")):
		r = rule{places.Active, "karting", t["indoor"] == "yes", false, ""}
	case t["leisure"] == "trampoline_park" || t["sport"] == "trampoline":
		r = rule{places.Active, "trampoline", true, false, ""}

	// --- Культура ---
	case t["tourism"] == "museum":
		r = rule{places.Culture, "museum", true, false, ""}
	case t["amenity"] == "theatre":
		r = rule{places.Culture, "theatre", true, false, ""}
	case t["tourism"] == "gallery" || t["amenity"] == "arts_centre":
		r = rule{places.Culture, "gallery", true, false, ""}
	case t["highway"] == "pedestrian" && inCity:
		if e.SizeMeters() < 400 {
			return r, false
		}
		r = rule{places.Culture, "street", false, true, ""}

	// --- Природа ---
	case t["natural"] == "peak":
		if !inMountains || t["ele"] == "" {
			return r, false
		}
		r = rule{places.Nature, "peak", false, true, ""}
	case t["waterway"] == "waterfall":
		r = rule{places.Nature, "waterfall", false, true, ""}
	case t["tourism"] == "viewpoint":
		if !inMountains && !inCity {
			return r, false
		}
		r = rule{places.Nature, "viewpoint", false, true, ""}
	case t["natural"] == "valley" || t["natural"] == "gorge":
		if !inMountains {
			return r, false
		}
		r = rule{places.Nature, "gorge", false, true, ""}
	case t["natural"] == "water" && (t["water"] == "lake" || t["water"] == "reservoir"):
		// в городе — только заметные водоёмы; за городом — именованные озёра
		if inCity && e.SizeMeters() < 300 {
			return r, false
		}
		r = rule{places.Nature, "lake", false, true, ""}
	case t["leisure"] == "garden" && (t["garden:type"] == "botanical" || strings.Contains(strings.ToLower(name), "ботанич")):
		r = rule{places.Nature, "garden", false, false, ""}
	case t["leisure"] == "park":
		// скверы во дворах и у остановок отсекаем по размеру
		if e.SizeMeters() < 350 {
			return r, false
		}
		r = rule{places.Nature, "park", false, true, ""}
	case t["leisure"] == "nature_reserve" || t["boundary"] == "national_park":
		r = rule{places.Nature, "park", false, false, ""}
	default:
		return r, false
	}

	switch t["fee"] {
	case "yes":
		r.free = false
	case "no":
		r.free = true
	}
	if r.season == "" {
		r.season = "all_year"
	}
	return r, true
}
