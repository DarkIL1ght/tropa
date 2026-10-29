package places

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"tropa/internal/notify"
)

// Места от пользователей: «Предложить место» создаёт черновик с автором,
// админ публикует его (как есть или поправив) или отклоняет с причиной.
// Автор и админы узнают об этом в Telegram.

var (
	// ErrTooManyPending — у автора уже MaxPending мест на проверке.
	ErrTooManyPending = errors.New("слишком много мест ждут проверки")
	// ErrReviewed — место уже опубликовано или отклонено.
	ErrReviewed = errors.New("место уже проверено")
)

// MaxPending — сколько мест одного автора может ждать проверки сразу.
const MaxPending = 10

// Suggested — место от пользователя со статусом модерации.
type Suggested struct {
	Place
	AuthorID     int64
	AuthorName   string // пусто — автор удалил аккаунт
	RejectReason string
	CreatedAt    time.Time
	ReviewedAt   *time.Time
}

// StatusLabel — статус для автора.
func (p *Suggested) StatusLabel() string {
	switch p.Status {
	case Draft:
		return "На проверке"
	case Published:
		return "Опубликовано"
	case Rejected:
		return "Отклонено"
	}
	return "Скрыто"
}

// Потолки полей из формы.
const (
	MaxAddress = 200
	MaxWebsite = 300
)

// prepare проверяет поля, которые заполняет человек: группу, вид, точку
// (по ней же — город или область), название, описание, сайт. Slug,
// источник и статус выставляет сервер.
func prepare(p *Place, slug string, status Status) map[string]string {
	p.Slug, p.Source, p.OSMRef, p.Status, p.OpeningHours = slug, "manual", "", status, ""
	p.Area = AreaOf(p.Lat, p.Lon)
	p.Normalize()
	errs := p.Validate()
	delete(errs, "slug")
	if errs["coords"] != "" || errs["area"] != "" {
		delete(errs, "area")
		errs["coords"] = "Поставьте точку в Алматы или Алматинской области"
	}
	if utf8.RuneCountInString(p.Address) > MaxAddress {
		errs["address"] = fmt.Sprintf("адрес — не длиннее %d символов", MaxAddress)
	}
	if len(p.Website) > MaxWebsite {
		errs["website"] = fmt.Sprintf("адрес сайта — не длиннее %d символов", MaxWebsite)
	}
	return errs
}

// Suggest сохраняет предложение автора черновиком и сообщает админам.
// Ошибки полей — в map (вместе с ErrInvalid).
func (s *Service) Suggest(ctx context.Context, authorID int64, authorName string, p *Place) (map[string]string, error) {
	if errs := prepare(p, "draft", Draft); len(errs) > 0 {
		return errs, ErrInvalid
	}
	base := Slugify(p.Name)
	if len(base) < 2 {
		base = "place"
	}
	err := pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		// строка автора под замком: две формы сразу не обойдут MaxPending.
		// Считаем отдельным запросом — после замка, со свежим снимком.
		if _, err := tx.Exec(ctx, `SELECT 1 FROM users WHERE id = $1 FOR UPDATE`, authorID); err != nil {
			return err
		}
		var pending int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM places WHERE author_id = $1 AND status = 'draft'`,
			authorID).Scan(&pending); err != nil {
			return err
		}
		if pending >= MaxPending {
			return ErrTooManyPending
		}
		// свободный slug: base, base-2, base-3…; ON CONFLICT не обрывает транзакцию
		for i := 1; ; i++ {
			p.Slug = base
			if i > 1 {
				p.Slug = fmt.Sprintf("%s-%d", base, i)
			}
			err := tx.QueryRow(ctx, `
				INSERT INTO places (slug, name, category, kind, lat, lon, area, address, description,
				                    indoor, free, season, website, source, status, author_id, created_at, updated_at)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, 'manual', 'draft', $14, $15, $15)
				ON CONFLICT (slug) DO NOTHING
				RETURNING id`,
				p.Slug, p.Name, p.Category, p.Kind, p.Lat, p.Lon, p.Area, p.Address, p.Description,
				p.Indoor, p.Free, p.Season, p.Website, authorID, s.Now()).Scan(&p.ID)
			if errors.Is(err, pgx.ErrNoRows) {
				continue
			}
			if err != nil {
				return err
			}
			break
		}
		return s.notifyAdmins(ctx, tx, authorID, fmt.Sprintf("Новое место на проверке: «%s» (%s, %s) от %s.\nМодерация: %s/admin/places",
			p.Name, p.Kind.Label(), p.Category.Label(), authorName, s.SiteURL))
	})
	return nil, err
}

// notifyAdmins ставит уведомление всем админам, кроме самого автора.
func (s *Service) notifyAdmins(ctx context.Context, tx pgx.Tx, except int64, text string) error {
	rows, err := tx.Query(ctx, `SELECT id FROM users WHERE role = 'admin' AND id <> $1`, except)
	if err != nil {
		return err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err := notify.Enqueue(ctx, tx, id, "place_suggested", text, ""); err != nil {
			return err
		}
	}
	return nil
}

const suggestedColumns = `p.id, p.slug, p.name, p.category, p.kind, p.lat, p.lon, p.area, p.address, p.description,
	p.indoor, p.free, p.season, p.website, p.opening_hours, p.source, coalesce(p.osm_ref, ''), p.status,
	coalesce(p.author_id, 0), coalesce(u.name, ''), p.reject_reason, p.created_at, p.reviewed_at`

func scanSuggested(row pgx.Row) (*Suggested, error) {
	p := &Suggested{}
	err := row.Scan(&p.ID, &p.Slug, &p.Name, &p.Category, &p.Kind, &p.Lat, &p.Lon, &p.Area,
		&p.Address, &p.Description, &p.Indoor, &p.Free, &p.Season, &p.Website, &p.OpeningHours,
		&p.Source, &p.OSMRef, &p.Status, &p.AuthorID, &p.AuthorName, &p.RejectReason, &p.CreatedAt, &p.ReviewedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return p, err
}

func (s *Service) listSuggested(ctx context.Context, where string, args ...any) ([]*Suggested, error) {
	rows, err := s.DB.Query(ctx, `SELECT `+suggestedColumns+`
		FROM places p LEFT JOIN users u ON u.id = p.author_id WHERE `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Suggested
	for rows.Next() {
		p, err := scanSuggested(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ByAuthor — последние предложения автора, новые сверху.
func (s *Service) ByAuthor(ctx context.Context, authorID int64, limit int) ([]*Suggested, error) {
	return s.listSuggested(ctx, `p.author_id = $1 ORDER BY p.created_at DESC, p.id DESC LIMIT $2`, authorID, limit)
}

// Pending — очередь модерации, старые сверху. Черновики из seed-файлов
// (без автора) сюда не попадают: их статус ведут файлы, а не админка.
func (s *Service) Pending(ctx context.Context, limit int) ([]*Suggested, error) {
	return s.listSuggested(ctx, `p.status = 'draft' AND p.author_id IS NOT NULL ORDER BY p.created_at, p.id LIMIT $1`, limit)
}

// Reviewed — недавно проверенные предложения, для истории в админке.
func (s *Service) Reviewed(ctx context.Context, limit int) ([]*Suggested, error) {
	return s.listSuggested(ctx, `p.author_id IS NOT NULL AND p.reviewed_at IS NOT NULL
		ORDER BY p.reviewed_at DESC, p.id DESC LIMIT $1`, limit)
}

// PendingByID — черновик на проверке по id (страница правки в админке).
func (s *Service) PendingByID(ctx context.Context, id int64) (*Suggested, error) {
	return scanSuggested(s.DB.QueryRow(ctx, `SELECT `+suggestedColumns+`
		FROM places p LEFT JOIN users u ON u.id = p.author_id WHERE p.id = $1 AND p.status = 'draft' AND p.author_id IS NOT NULL`, id))
}

// lockDraft — черновик под FOR UPDATE: два админа не проверят его одновременно.
func lockDraft(ctx context.Context, tx pgx.Tx, id int64) (*Suggested, error) {
	p, err := scanSuggested(tx.QueryRow(ctx, `SELECT `+suggestedColumns+`
		FROM places p LEFT JOIN users u ON u.id = p.author_id WHERE p.id = $1 AND p.author_id IS NOT NULL FOR UPDATE OF p`, id))
	if err != nil {
		return nil, err
	}
	if p.Status != Draft {
		return nil, ErrReviewed
	}
	return p, nil
}

// Publish публикует черновик. edit — поля после правки админом; nil —
// как прислал автор. Ошибки полей — в map (вместе с ErrInvalid).
func (s *Service) Publish(ctx context.Context, id int64, edit *Place) (map[string]string, error) {
	var errs map[string]string
	err := pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		cur, err := lockDraft(ctx, tx, id)
		if err != nil {
			return err
		}
		p := cur.Place
		if edit != nil {
			p.Name, p.Category, p.Kind, p.Lat, p.Lon = edit.Name, edit.Category, edit.Kind, edit.Lat, edit.Lon
			p.Address, p.Description, p.Website = edit.Address, edit.Description, edit.Website
			p.Indoor, p.Free, p.Season = edit.Indoor, edit.Free, edit.Season
		}
		if errs = prepare(&p, cur.Slug, Published); len(errs) > 0 {
			return ErrInvalid
		}
		if _, err := tx.Exec(ctx, `UPDATE places SET name = $2, category = $3, kind = $4, lat = $5, lon = $6,
			area = $7, address = $8, description = $9, indoor = $10, free = $11, season = $12, website = $13,
			status = 'published', reviewed_at = $14, updated_at = $14 WHERE id = $1`,
			id, p.Name, p.Category, p.Kind, p.Lat, p.Lon, p.Area, p.Address, p.Description,
			p.Indoor, p.Free, p.Season, p.Website, s.Now()); err != nil {
			return err
		}
		if cur.AuthorID == 0 {
			return nil
		}
		return notify.Enqueue(ctx, tx, cur.AuthorID, "place_published",
			fmt.Sprintf("Ваше место «%s» опубликовано в Tropa — спасибо!\n%s/places/%s", p.Name, s.SiteURL, p.Slug), "")
	})
	if errors.Is(err, ErrInvalid) {
		return errs, err
	}
	return nil, err
}

// MaxReason — потолок причины отказа.
const MaxReason = 500

// Reject отклоняет черновик с причиной — её увидит автор.
func (s *Service) Reject(ctx context.Context, id int64, reason string) error {
	reason = strings.TrimSpace(reason)
	if n := utf8.RuneCountInString(reason); n < 3 || n > MaxReason {
		return ErrInvalid
	}
	return pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		cur, err := lockDraft(ctx, tx, id)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE places SET status = 'rejected', reject_reason = $2,
			reviewed_at = $3, updated_at = $3 WHERE id = $1`, id, reason, s.Now()); err != nil {
			return err
		}
		if cur.AuthorID == 0 {
			return nil
		}
		return notify.Enqueue(ctx, tx, cur.AuthorID, "place_rejected",
			fmt.Sprintf("Место «%s» не опубликовано. Причина: %s\nВаши предложения: %s/places/suggest", cur.Name, reason, s.SiteURL), "")
	})
}
