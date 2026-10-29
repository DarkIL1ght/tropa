package places

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrNotFound = errors.New("место не найдено")
	ErrInvalid  = errors.New("неверные данные места")
)

type Service struct {
	DB  *pgxpool.Pool
	Now func() time.Time
	// SiteURL — адрес сайта без / в конце, для ссылок в уведомлениях.
	SiteURL string
}

func New(db *pgxpool.Pool) *Service { return &Service{DB: db, Now: time.Now} }

// UpsertResult — что сделал Upsert со строкой.
type UpsertResult int

const (
	Unchanged UpsertResult = iota
	Inserted
	Updated
)

// Upsert вставляет место или обновляет его по slug. Если поля не
// изменились, строка не трогается (updated_at остаётся прежним) —
// повторный seed ничего не меняет.
func (s *Service) Upsert(ctx context.Context, p *Place) (UpsertResult, error) {
	return upsert(ctx, s.DB, p)
}

type querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func upsert(ctx context.Context, db querier, p *Place) (UpsertResult, error) {
	p.Normalize()
	if errs := p.Validate(); len(errs) > 0 {
		return Unchanged, fmt.Errorf("%w %s: %v", ErrInvalid, p.Slug, errs)
	}
	var osmRef *string
	if p.OSMRef != "" {
		osmRef = &p.OSMRef
	}
	var inserted bool
	err := db.QueryRow(ctx, `
		INSERT INTO places (slug, name, category, kind, lat, lon, area, address, description,
		                    indoor, free, season, website, opening_hours, source, osm_ref, status)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17)
		ON CONFLICT (slug) DO UPDATE SET
		    name = EXCLUDED.name, category = EXCLUDED.category, kind = EXCLUDED.kind,
		    lat = EXCLUDED.lat, lon = EXCLUDED.lon, area = EXCLUDED.area,
		    address = EXCLUDED.address, description = EXCLUDED.description,
		    indoor = EXCLUDED.indoor, free = EXCLUDED.free, season = EXCLUDED.season,
		    website = EXCLUDED.website, opening_hours = EXCLUDED.opening_hours,
		    source = EXCLUDED.source, osm_ref = EXCLUDED.osm_ref, status = EXCLUDED.status,
		    updated_at = now()
		-- места от пользователей seed не перезаписывает, даже при совпадении slug
		WHERE places.author_id IS NULL AND (places.name, places.category, places.kind, places.lat, places.lon, places.area,
		       places.address, places.description, places.indoor, places.free, places.season,
		       places.website, places.opening_hours, places.source, places.osm_ref, places.status)
		      IS DISTINCT FROM
		      (EXCLUDED.name, EXCLUDED.category, EXCLUDED.kind, EXCLUDED.lat, EXCLUDED.lon, EXCLUDED.area,
		       EXCLUDED.address, EXCLUDED.description, EXCLUDED.indoor, EXCLUDED.free, EXCLUDED.season,
		       EXCLUDED.website, EXCLUDED.opening_hours, EXCLUDED.source, EXCLUDED.osm_ref, EXCLUDED.status)
		RETURNING id, (xmax = 0)`, // xmax = 0 — строка только что вставлена
		p.Slug, p.Name, p.Category, p.Kind, p.Lat, p.Lon, p.Area, p.Address, p.Description,
		p.Indoor, p.Free, p.Season, p.Website, p.OpeningHours, p.Source, osmRef, p.Status,
	).Scan(&p.ID, &inserted)
	switch {
	case errors.Is(err, pgx.ErrNoRows): // конфликт, но WHERE отсёк — данные те же или место от пользователя
		return Unchanged, nil
	case err != nil:
		return Unchanged, fmt.Errorf("место %s: %w", p.Slug, err)
	case inserted:
		return Inserted, nil
	default:
		return Updated, nil
	}
}

const placeColumns = `id, slug, name, category, kind, lat, lon, area, address, description,
	indoor, free, season, website, opening_hours, source, coalesce(osm_ref, ''), status`

func scanPlace(row pgx.Row) (*Place, error) {
	p := &Place{}
	err := row.Scan(&p.ID, &p.Slug, &p.Name, &p.Category, &p.Kind, &p.Lat, &p.Lon, &p.Area,
		&p.Address, &p.Description, &p.Indoor, &p.Free, &p.Season, &p.Website, &p.OpeningHours,
		&p.Source, &p.OSMRef, &p.Status)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return p, err
}

// Get — опубликованное место по slug.
func (s *Service) Get(ctx context.Context, slug string) (*Place, error) {
	return scanPlace(s.DB.QueryRow(ctx,
		`SELECT `+placeColumns+` FROM places WHERE slug = $1 AND status = 'published'`, slug))
}

// Query — фильтр карты. Нулевые поля не ограничивают.
type Query struct {
	BBox       *BBox
	Categories []Category
	IndoorOnly bool
	FreeOnly   bool
	Text       string
	Limit      int // по умолчанию и максимум — MaxResults
}

// MaxResults — потолок мест в одном ответе карты.
const MaxResults = 2000

func likeEscape(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// Search — опубликованные места по фильтру, по названию.
func (s *Service) Search(ctx context.Context, q Query) ([]*Place, error) {
	where := []string{"status = 'published'"}
	args := []any{}
	arg := func(v any) string {
		args = append(args, v)
		return fmt.Sprintf("$%d", len(args))
	}
	if b := q.BBox; b != nil {
		where = append(where, fmt.Sprintf("lat BETWEEN %s AND %s AND lon BETWEEN %s AND %s",
			arg(b.South), arg(b.North), arg(b.West), arg(b.East)))
	}
	if len(q.Categories) > 0 {
		cats := make([]string, len(q.Categories))
		for i, c := range q.Categories {
			cats[i] = string(c)
		}
		where = append(where, "category = ANY("+arg(cats)+")")
	}
	if q.IndoorOnly {
		where = append(where, "indoor")
	}
	if q.FreeOnly {
		where = append(where, "free")
	}
	if t := strings.TrimSpace(q.Text); t != "" {
		p := "%" + likeEscape(t) + "%"
		where = append(where, "(name ILIKE "+arg(p)+" OR address ILIKE "+arg(p)+")")
	}
	limit := q.Limit
	if limit <= 0 || limit > MaxResults {
		limit = MaxResults
	}
	rows, err := s.DB.Query(ctx, `SELECT `+placeColumns+` FROM places WHERE `+
		strings.Join(where, " AND ")+` ORDER BY name, id LIMIT `+arg(limit), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Place
	for rows.Next() {
		p, err := scanPlace(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
