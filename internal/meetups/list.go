package meetups

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
)

// PageSize — встреч на странице списка.
const PageSize = 12

// Filter — параметры списка встреч из строки запроса.
type Filter struct {
	Query    string // поиск по названию встречи и места
	Category string // группа места; пусто — любая
	Page     int    // с 1
}

// ParseFilter читает фильтр из query-параметров; неверные значения молча
// игнорируются — список должен открываться по любой ссылке.
// categories — допустимые группы мест.
func ParseFilter(q url.Values, categories []string) Filter {
	f := Filter{Query: strings.TrimSpace(q.Get("q")), Page: 1}
	if c := q.Get("cat"); c != "" {
		for _, ok := range categories {
			if c == ok {
				f.Category = c
			}
		}
	}
	if len([]rune(f.Query)) > 100 {
		f.Query = string([]rune(f.Query)[:100])
	}
	if p, err := strconv.Atoi(q.Get("page")); err == nil && p > 1 && p < 10_000 {
		f.Page = p
	}
	return f
}

// Values — фильтр обратно в query-параметры (для ссылок пагинации).
func (f Filter) Values() url.Values {
	v := url.Values{}
	if f.Query != "" {
		v.Set("q", f.Query)
	}
	if f.Category != "" {
		v.Set("cat", f.Category)
	}
	return v
}

// PageURL — ссылка на страницу n списка с тем же фильтром.
func (f Filter) PageURL(n int) string {
	v := f.Values()
	if n > 1 {
		v.Set("page", strconv.Itoa(n))
	}
	if len(v) == 0 {
		return "/meetups"
	}
	return "/meetups?" + v.Encode()
}

type Page struct {
	Meetups    []*Meetup
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

// upcomingPublic — условие «открытая, не отменённая, ещё не началась».
const upcomingPublic = `m.visibility = 'public' AND m.status <> 'cancelled' AND m.starts_at > $1`

// List — предстоящие открытые встречи по фильтру, ближайшие сверху.
func (s *Service) List(ctx context.Context, f Filter) (Page, error) {
	args := []any{s.Now()}
	arg := func(v any) string {
		args = append(args, v)
		return fmt.Sprintf("$%d", len(args))
	}
	cond := upcomingPublic
	if f.Query != "" {
		p := "%" + likeEscape(f.Query) + "%"
		cond += " AND (m.title ILIKE " + arg(p) + " OR p.name ILIKE " + arg(p) + ")"
	}
	if f.Category != "" {
		cond += " AND p.category = " + arg(f.Category)
	}

	var pg Page
	if err := s.DB.QueryRow(ctx, `SELECT count(*)`+meetupFrom+` WHERE `+cond, args...).Scan(&pg.Total); err != nil {
		return pg, err
	}
	pg.TotalPages = max(1, (pg.Total+PageSize-1)/PageSize)
	pg.Page = min(max(f.Page, 1), pg.TotalPages)
	sql := `SELECT ` + meetupColumns + meetupFrom + ` WHERE ` + cond + ` ORDER BY m.starts_at, m.id` +
		` LIMIT ` + arg(PageSize) + ` OFFSET ` + arg((pg.Page-1)*PageSize)
	var err error
	pg.Meetups, err = s.collect(ctx, sql, args...)
	return pg, err
}

// ByPlace — ближайшие открытые встречи на месте (для страницы места).
func (s *Service) ByPlace(ctx context.Context, placeID int64) ([]*Meetup, error) {
	return s.collect(ctx, `SELECT `+meetupColumns+meetupFrom+` WHERE `+upcomingPublic+
		` AND m.place_id = $2 ORDER BY m.starts_at, m.id LIMIT 20`, s.Now(), placeID)
}

// CountByPlace — число предстоящих открытых встреч по id места (для карты).
// Места без встреч в ответ не попадают.
func (s *Service) CountByPlace(ctx context.Context, placeIDs []int64) (map[int64]int, error) {
	out := map[int64]int{}
	if len(placeIDs) == 0 {
		return out, nil
	}
	rows, err := s.DB.Query(ctx, `SELECT m.place_id, count(*) FROM meetups m
		WHERE `+upcomingPublic+` AND m.place_id = ANY($2) GROUP BY m.place_id`, s.Now(), placeIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[id] = n
	}
	return out, rows.Err()
}

// ByOrganizer — все встречи организатора, новые сверху (для профиля).
func (s *Service) ByOrganizer(ctx context.Context, userID int64) ([]*Meetup, error) {
	return s.collect(ctx, `SELECT `+meetupColumns+meetupFrom+
		` WHERE m.organizer_id = $1 ORDER BY m.starts_at DESC, m.id DESC`, userID)
}

// ByFriends — предстоящие встречи, которые собирают друзья пользователя,
// включая «для друзей»: ради этого дружба и нужна. Ближайшие сверху.
func (s *Service) ByFriends(ctx context.Context, userID int64) ([]*Meetup, error) {
	return s.collect(ctx, `SELECT `+meetupColumns+meetupFrom+`
		WHERE m.status <> 'cancelled' AND m.starts_at > $1
		  AND m.organizer_id IN (
		      SELECT CASE WHEN f.user_id = $2 THEN f.friend_id ELSE f.user_id END
		        FROM friendships f WHERE (f.user_id = $2 OR f.friend_id = $2) AND f.status = 'accepted')
		ORDER BY m.starts_at, m.id LIMIT 50`, s.Now(), userID)
}

// Recent — последние созданные встречи любой видимости и статуса (для админки).
func (s *Service) Recent(ctx context.Context, limit int) ([]*Meetup, error) {
	return s.collect(ctx, `SELECT `+meetupColumns+meetupFrom+` ORDER BY m.created_at DESC, m.id DESC LIMIT $1`, limit)
}

// PublicByOrganizer — предстоящие открытые встречи организатора (для его
// публичной страницы). Встречи «для друзей» сюда не попадают никогда.
func (s *Service) PublicByOrganizer(ctx context.Context, userID int64) ([]*Meetup, error) {
	return s.collect(ctx, `SELECT `+meetupColumns+meetupFrom+` WHERE `+upcomingPublic+
		` AND m.organizer_id = $2 ORDER BY m.starts_at, m.id LIMIT 20`, s.Now(), userID)
}

// Stats — счётчики для профиля. Считаются все встречи, включая «для друзей»:
// число ничего о них не раскрывает.
type Stats struct {
	Organized int // собрал: не отменённые, в том числе будущие
	Attended  int // был: прошедшие чужие, где был записан и шёл
}

func (s *Service) Stats(ctx context.Context, userID int64) (Stats, error) {
	var st Stats
	err := s.DB.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM meetups WHERE organizer_id = $1 AND status <> 'cancelled'),
		(SELECT count(*) FROM participants p JOIN meetups m ON m.id = p.meetup_id
		  WHERE p.user_id = $1 AND p.status = 'confirmed' AND m.organizer_id <> $1
		    AND m.status <> 'cancelled' AND m.starts_at <= $2)`, userID, s.Now()).Scan(&st.Organized, &st.Attended)
	return st, err
}

// Joined — встреча, на которую записан пользователь, и его статус.
type Joined struct {
	*Meetup
	Me Participant
}

// ByParticipant — чужие встречи, куда записан пользователь (для профиля):
// сначала предстоящие по времени, затем прошедшие.
func (s *Service) ByParticipant(ctx context.Context, userID int64) ([]Joined, error) {
	rows, err := s.DB.Query(ctx, `SELECT `+meetupColumns+`,
		       x.status, coalesce(x.position, 0), coalesce(x.offer_expires_at, 'epoch')`+meetupFrom+`
		  JOIN participants x ON x.meetup_id = m.id AND x.user_id = $1
		 WHERE m.organizer_id <> $1
		 ORDER BY m.starts_at <= $2::timestamptz, abs(extract(epoch FROM m.starts_at - $2::timestamptz)), m.id`, userID, s.Now())
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Joined, error) {
		j := Joined{Meetup: &Meetup{}, Me: Participant{UserID: userID}}
		m := j.Meetup
		err := row.Scan(&m.ID, &m.Token, &m.PlaceID, &m.PlaceSlug, &m.PlaceName, &m.PlaceCategory,
			&m.OrganizerID, &m.OrganizerName, &m.Title, &m.Description, &m.StartsAt, &m.Capacity,
			&m.Visibility, &m.Status, &m.CreatedAt, &m.Going, &m.Waiting,
			&j.Me.Status, &j.Me.Position, &j.Me.OfferExpiresAt)
		return j, err
	})
}

func (s *Service) collect(ctx context.Context, sql string, args ...any) ([]*Meetup, error) {
	rows, err := s.DB.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Meetup
	for rows.Next() {
		m, err := scanMeetup(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
