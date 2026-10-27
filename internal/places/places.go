// Package places — места на карте: модель, проверка, загрузка seed-файлов, поиск.
package places

import (
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"
)

type Category string

const (
	Nature  Category = "nature"
	Active  Category = "active"
	Indoor  Category = "indoor"
	Culture Category = "culture"
)

// Categories — порядок групп в фильтрах и легенде.
var Categories = []Category{Nature, Active, Indoor, Culture}

var categoryLabels = map[Category]string{
	Nature: "Природа", Active: "Активный отдых", Indoor: "В тепле", Culture: "Культура",
}

func (c Category) Label() string { return categoryLabels[c] }

// kinds — допустимые виды мест в каждой группе и их подписи.
var kinds = map[Category][]Kind{
	Nature:  {"trail", "peak", "gorge", "lake", "waterfall", "park", "garden", "viewpoint", "canyon"},
	Active:  {"rink", "ski", "climbing", "bowling", "karting", "trampoline", "sports"},
	Indoor:  {"anticafe", "boardgames", "quest", "cinema", "karaoke", "cafe", "mall"},
	Culture: {"museum", "theatre", "gallery", "street", "landmark"},
}

type Kind string

var kindLabels = map[Kind]string{
	"trail": "Тропа", "peak": "Пик", "gorge": "Ущелье", "lake": "Озеро", "waterfall": "Водопад",
	"park": "Парк", "garden": "Сад", "viewpoint": "Смотровая", "canyon": "Каньон",
	"rink": "Каток", "ski": "Горнолыжный курорт", "climbing": "Скалодром", "bowling": "Боулинг",
	"karting": "Картинг", "trampoline": "Батуты", "sports": "Спорт",
	"anticafe": "Антикафе", "boardgames": "Настольные игры", "quest": "Квесты", "cinema": "Кино",
	"karaoke": "Караоке", "cafe": "Кафе", "mall": "ТРЦ",
	"museum": "Музей", "theatre": "Театр", "gallery": "Галерея", "street": "Прогулочная улица",
	"landmark": "Достопримечательность",
}

func (k Kind) Label() string { return kindLabels[k] }

// Kinds — виды мест группы c.
func Kinds(c Category) []Kind { return kinds[c] }

func validKind(c Category, k Kind) bool { return slices.Contains(kinds[c], k) }

type Area string

const (
	Almaty Area = "almaty"
	Region Area = "region"
)

type Season string

var seasonLabels = map[Season]string{"all_year": "Круглый год", "summer": "Летом", "winter": "Зимой"}

func (s Season) Label() string { return seasonLabels[s] }

// BBox — прямоугольник координат.
type BBox struct{ South, West, North, East float64 }

func (b BBox) Contains(lat, lon float64) bool {
	return lat >= b.South && lat <= b.North && lon >= b.West && lon <= b.East
}

var (
	// RegionBBox — рамка вокруг Алматинской области в границах после 2022 года.
	// Только для быстрого отсева и рамки карты: точная проверка — InArea.
	RegionBBox = BBox{South: 42.1, West: 74.0, North: 46.8, East: 80.6}
	// CityBBox — рамка вокруг Алматы (точная проверка — InCity).
	// Юг — до верховий Малой Алматинки (пик Горельник ≈ 43.04).
	CityBBox = BBox{South: 43.00, West: 76.70, North: 43.45, East: 77.20}
)

type Status string

const (
	Draft     Status = "draft"
	Published Status = "published"
	Hidden    Status = "hidden"
)

// Place — место. Теги json — формат seed-файлов data/places/*.json.
type Place struct {
	ID           int64    `json:"-"`
	Slug         string   `json:"slug"`
	Name         string   `json:"name"`
	Category     Category `json:"category"`
	Kind         Kind     `json:"kind"`
	Lat          float64  `json:"lat"`
	Lon          float64  `json:"lon"`
	Area         Area     `json:"area"`
	Address      string   `json:"address,omitempty"`
	Description  string   `json:"description,omitempty"`
	Indoor       bool     `json:"indoor,omitempty"`
	Free         bool     `json:"free,omitempty"`
	Season       Season   `json:"season,omitempty"`
	Website      string   `json:"website,omitempty"`
	OpeningHours string   `json:"opening_hours,omitempty"`
	Source       string   `json:"source,omitempty"`
	OSMRef       string   `json:"osm_ref,omitempty"`
	Status       Status   `json:"status,omitempty"`
}

var (
	slugRe   = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
	osmRefRe = regexp.MustCompile(`^(node|way|relation)/[0-9]+$`)
)

// Normalize подставляет значения по умолчанию и обрезает пробелы.
func (p *Place) Normalize() {
	p.Slug = strings.TrimSpace(p.Slug)
	p.Name = strings.TrimSpace(p.Name)
	p.Address = strings.TrimSpace(p.Address)
	p.Description = strings.TrimSpace(p.Description)
	p.Website = strings.TrimSpace(p.Website)
	p.OpeningHours = strings.TrimSpace(p.OpeningHours)
	if p.Season == "" {
		p.Season = "all_year"
	}
	if p.Source == "" {
		p.Source = "manual"
	}
	if p.Status == "" {
		p.Status = Published
	}
}

// Validate проверяет нормализованное место; ошибки — по полям.
func (p *Place) Validate() map[string]string {
	errs := map[string]string{}
	if len(p.Slug) < 2 || len(p.Slug) > 80 || !slugRe.MatchString(p.Slug) {
		errs["slug"] = "slug — латиница, цифры и дефисы, 2–80 символов"
	}
	if n := utf8.RuneCountInString(p.Name); n < 2 || n > 120 {
		errs["name"] = "название — от 2 до 120 символов"
	}
	if p.Category.Label() == "" {
		errs["category"] = "неизвестная группа"
	} else if !validKind(p.Category, p.Kind) {
		errs["kind"] = fmt.Sprintf("вид %q не из группы %s", p.Kind, p.Category)
	}
	actual := AreaOf(p.Lat, p.Lon)
	switch {
	case p.Area != Almaty && p.Area != Region:
		errs["area"] = "area — almaty или region"
	case actual == "":
		errs["coords"] = "координаты вне Алматы и Алматинской области"
	case actual != p.Area:
		errs["coords"] = fmt.Sprintf("по координатам это %s, а указано area=%s", actual, p.Area)
	}
	if p.Season.Label() == "" {
		errs["season"] = "season — all_year, summer или winter"
	}
	if utf8.RuneCountInString(p.Description) > 3000 {
		errs["description"] = "описание — не длиннее 3000 символов"
	}
	if p.Website != "" {
		if u, err := url.Parse(p.Website); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			errs["website"] = "сайт — полный адрес http(s)://"
		}
	}
	if p.Source != "manual" && p.Source != "osm" {
		errs["source"] = "source — manual или osm"
	}
	if p.OSMRef != "" && !osmRefRe.MatchString(p.OSMRef) {
		errs["osm_ref"] = "osm_ref — node/N, way/N или relation/N"
	}
	if p.Source == "osm" && p.OSMRef == "" {
		errs["osm_ref"] = "у места из OSM нужен osm_ref"
	}
	if p.Status != Draft && p.Status != Published && p.Status != Hidden {
		errs["status"] = "status — draft, published или hidden"
	}
	return errs
}

// TwoGISURL — ссылка «Открыть в 2ГИС»: поиск по названию рядом с точкой.
// Это обычная ссылка на сайт 2ГИС, без API и ключа.
func (p *Place) TwoGISURL() string {
	city := "almaty"
	return fmt.Sprintf("https://2gis.kz/%s/search/%s?m=%.6f%%2C%.6f%%2F16",
		city, url.PathEscape(p.Name), p.Lon, p.Lat)
}
