package osm

import (
	"fmt"
	"math"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"tropa/internal/places"
)

// Existing — уже принятые места: их osm_ref и точки для поиска дублей.
type Existing struct {
	refs   map[string]bool
	slugs  map[string]bool
	points []namedPoint
}

type namedPoint struct {
	name     string
	lat, lon float64
}

func NewExisting(list []*places.Place) *Existing {
	ex := &Existing{refs: map[string]bool{}, slugs: map[string]bool{}}
	for _, p := range list {
		if p.OSMRef != "" {
			ex.refs[p.OSMRef] = true
		}
		ex.slugs[p.Slug] = true
		ex.points = append(ex.points, namedPoint{strings.ToLower(p.Name), p.Lat, p.Lon})
	}
	return ex
}

// near — расстояние в метрах (равнопромежуточная проекция: для сотен метров точно).
func near(lat1, lon1, lat2, lon2 float64) float64 {
	const r = 6_371_000.0
	x := (lon2 - lon1) * math.Pi / 180 * math.Cos((lat1+lat2)/2*math.Pi/180)
	y := (lat2 - lat1) * math.Pi / 180
	return r * math.Hypot(x, y)
}

// duplicate — место с тем же osm_ref или с тем же названием в пределах 700 м.
func (ex *Existing) duplicate(ref, name string, lat, lon float64) bool {
	if ex.refs[ref] {
		return true
	}
	ln := strings.ToLower(name)
	for _, p := range ex.points {
		if p.name == ln && near(lat, lon, p.lat, p.lon) < 700 {
			return true
		}
	}
	return false
}

func (ex *Existing) add(p *places.Place) {
	ex.refs[p.OSMRef] = true
	ex.slugs[p.Slug] = true
	ex.points = append(ex.points, namedPoint{strings.ToLower(p.Name), p.Lat, p.Lon})
}

// Candidates превращает элементы Overpass в черновики мест: классификация,
// координаты в области, без дублей между собой и с уже принятыми местами.
// Порядок — по группе, виду и названию, чтобы дифф файла был стабильным.
func Candidates(elems []Element, existing *Existing) []*places.Place {
	if existing == nil {
		existing = NewExisting(nil)
	}
	// relation и way надёжнее узлов-точек: обрабатываем их первыми
	order := map[string]int{"relation": 0, "way": 1, "node": 2}
	sorted := append([]Element(nil), elems...)
	sort.SliceStable(sorted, func(i, j int) bool { return order[sorted[i].Type] < order[sorted[j].Type] })

	var out []*places.Place
	for i := range sorted {
		e := &sorted[i]
		r, ok := Classify(e)
		if !ok {
			continue
		}
		lat, lon := e.Center()
		area := places.AreaOf(lat, lon)
		if area == "" { // Кыргызстан, Жетысуская область и т. п.
			continue
		}
		ref := e.Type + "/" + strconv.FormatInt(e.ID, 10)
		name := Quotes(e.Name())
		if existing.duplicate(ref, name, lat, lon) {
			continue
		}
		p := &places.Place{
			Name: name, Category: r.category, Kind: r.kind,
			Lat: round5(lat), Lon: round5(lon), Area: area,
			Address: address(e.Tags), Indoor: r.indoor, Free: r.free, Season: r.season,
			Website: website(e.Tags), OpeningHours: strings.TrimSpace(e.Tags["opening_hours"]),
			Source: "osm", OSMRef: ref, Status: places.Draft,
		}
		p.Slug = uniqueSlug(places.Slugify(name), existing)
		if p.Slug == "" {
			continue
		}
		p.Normalize()
		if len(p.Validate()) > 0 {
			continue
		}
		existing.add(p)
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Category != b.Category {
			return a.Category < b.Category
		}
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		return a.Name < b.Name
	})
	return out
}

// Quotes меняет пары прямых кавычек на ёлочки: ТРЦ "MART" → ТРЦ «MART».
func Quotes(s string) string {
	if strings.Count(s, `"`)%2 != 0 {
		return s
	}
	var b strings.Builder
	open := true
	for _, c := range s {
		if c == '"' {
			if open {
				b.WriteRune('«')
			} else {
				b.WriteRune('»')
			}
			open = !open
			continue
		}
		b.WriteRune(c)
	}
	return b.String()
}

func uniqueSlug(base string, ex *Existing) string {
	if len(base) < 2 {
		return ""
	}
	if !ex.slugs[base] {
		return base
	}
	for i := 2; ; i++ {
		s := fmt.Sprintf("%s-%d", base, i)
		if !ex.slugs[s] {
			return s
		}
	}
}

func round5(x float64) float64 { return math.Round(x*1e5) / 1e5 }

func address(t map[string]string) string {
	street, house := strings.TrimSpace(t["addr:street"]), strings.TrimSpace(t["addr:housenumber"])
	switch {
	case street != "" && house != "":
		return street + ", " + house
	default:
		return street
	}
}

// website — только корректные http(s)-адреса; мусор в теге лучше отбросить.
func website(t map[string]string) string {
	for _, k := range []string{"website", "contact:website", "url"} {
		w := strings.TrimSpace(t[k])
		if w == "" {
			continue
		}
		if !strings.Contains(w, "://") {
			w = "https://" + w
		}
		if u, err := url.Parse(w); err == nil && (u.Scheme == "http" || u.Scheme == "https") && strings.Contains(u.Host, ".") {
			return w
		}
	}
	return ""
}
