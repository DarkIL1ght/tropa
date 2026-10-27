package trips

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// PageSize — походов на странице каталога.
const PageSize = 12

// Filter — параметры каталога из строки запроса.
type Filter struct {
	Query      string     // поиск по названию и региону
	Region     string     // точное совпадение региона
	Difficulty Difficulty // пусто — любая
	From, To   time.Time  // поход начинается в этом интервале; нулевые — без границы
	Past       bool       // показывать прошедшие вместо предстоящих
	Page       int        // с 1
}

// ParseFilter читает фильтр из query-параметров; неверные значения
// молча игнорируются — каталог должен открываться по любой ссылке.
func ParseFilter(q url.Values) Filter {
	f := Filter{
		Query:  strings.TrimSpace(q.Get("q")),
		Region: strings.TrimSpace(q.Get("region")),
		Past:   q.Get("past") == "1",
		Page:   1,
	}
	if d := Difficulty(q.Get("difficulty")); d.Label() != "" {
		f.Difficulty = d
	}
	if t, err := time.Parse(DateLayout, q.Get("from")); err == nil {
		f.From = t
	}
	if t, err := time.Parse(DateLayout, q.Get("to")); err == nil {
		f.To = t
	}
	if p, err := strconv.Atoi(q.Get("page")); err == nil && p > 1 && p < 10_000 {
		f.Page = p
	}
	return f
}

// Values — фильтр обратно в query-параметры (для ссылок пагинации).
func (f Filter) Values() url.Values {
	v := url.Values{}
	set := func(k, val string) {
		if val != "" {
			v.Set(k, val)
		}
	}
	set("q", f.Query)
	set("region", f.Region)
	set("difficulty", string(f.Difficulty))
	if !f.From.IsZero() {
		v.Set("from", f.From.Format(DateLayout))
	}
	if !f.To.IsZero() {
		v.Set("to", f.To.Format(DateLayout))
	}
	if f.Past {
		v.Set("past", "1")
	}
	return v
}

// PageURL — ссылка на страницу n каталога с тем же фильтром.
func (f Filter) PageURL(n int) string {
	v := f.Values()
	if n > 1 {
		v.Set("page", strconv.Itoa(n))
	}
	if len(v) == 0 {
		return "/trips"
	}
	return "/trips?" + v.Encode()
}

type Page struct {
	Trips      []*Trip
	Total      int
	Page       int
	TotalPages int
}

func (p Page) HasPrev() bool { return p.Page > 1 }
func (p Page) HasNext() bool { return p.Page < p.TotalPages }

// likeEscape экранирует спецсимволы LIKE, чтобы «50%» искалось буквально.
func likeEscape(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// Catalog — публичные походы по фильтру. Предстоящие — по возрастанию
// даты начала (ещё не закончились), прошедшие — по убыванию.
func (s *Service) Catalog(ctx context.Context, f Filter) (Page, error) {
	where := []string{}
	args := []any{}
	arg := func(v any) string {
		args = append(args, v)
		return fmt.Sprintf("$%d", len(args))
	}

	today := s.Today()
	order := "t.start_date, t.id"
	if f.Past {
		where = append(where, "t.status = 'done' OR (t.status IN ('open','full','closed') AND t.end_date < "+arg(today)+")")
		order = "t.start_date DESC, t.id DESC"
	} else {
		where = append(where, "t.status IN ('open','full','closed') AND t.end_date >= "+arg(today))
	}
	if f.Query != "" {
		p := "%" + likeEscape(f.Query) + "%"
		where = append(where, "(t.title ILIKE "+arg(p)+" OR t.region ILIKE "+arg(p)+")")
	}
	if f.Region != "" {
		where = append(where, "lower(t.region) = lower("+arg(f.Region)+")")
	}
	if f.Difficulty != "" {
		where = append(where, "t.difficulty = "+arg(f.Difficulty))
	}
	if !f.From.IsZero() {
		where = append(where, "t.start_date >= "+arg(f.From))
	}
	if !f.To.IsZero() {
		where = append(where, "t.start_date <= "+arg(f.To))
	}
	cond := "(" + strings.Join(where, ") AND (") + ")"

	var p Page
	if err := s.DB.QueryRow(ctx, `SELECT count(*) FROM trips t WHERE `+cond, args...).Scan(&p.Total); err != nil {
		return p, err
	}
	p.TotalPages = max(1, (p.Total+PageSize-1)/PageSize)
	p.Page = min(max(f.Page, 1), p.TotalPages)

	sql := `SELECT ` + tripColumns + ` FROM trips t JOIN users u ON u.id = t.organizer_id
	         WHERE ` + cond + ` ORDER BY ` + order +
		` LIMIT ` + arg(PageSize) + ` OFFSET ` + arg((p.Page-1)*PageSize)
	rows, err := s.DB.Query(ctx, sql, args...)
	if err != nil {
		return p, err
	}
	defer rows.Close()
	for rows.Next() {
		t, err := scanTrip(rows)
		if err != nil {
			return p, err
		}
		p.Trips = append(p.Trips, t)
	}
	return p, rows.Err()
}

// Regions — регионы предстоящих публичных походов (для выпадающего списка).
func (s *Service) Regions(ctx context.Context) ([]string, error) {
	rows, err := s.DB.Query(ctx,
		`SELECT DISTINCT region FROM trips
		  WHERE status IN ('open','full','closed') AND end_date >= $1 ORDER BY region`, s.Today())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var r string
		if err := rows.Scan(&r); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ByOrganizer — все походы организатора, новые сверху (для профиля).
func (s *Service) ByOrganizer(ctx context.Context, userID int64) ([]*Trip, error) {
	rows, err := s.DB.Query(ctx,
		`SELECT `+tripColumns+` FROM trips t JOIN users u ON u.id = t.organizer_id
		  WHERE t.organizer_id = $1 ORDER BY t.start_date DESC, t.id DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Trip
	for rows.Next() {
		t, err := scanTrip(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
