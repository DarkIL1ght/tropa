package places

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
)

// LoadSeed читает все data/places/*.json, кроме candidates*.json (это
// черновой вывод osm-fetch). Проверяет каждое место и уникальность slug
// и osm_ref по всем файлам; при любой ошибке не возвращает ничего —
// чтобы seed не залил половину данных.
func LoadSeed(fsys fs.FS) ([]*Place, error) {
	names, err := fs.Glob(fsys, "*.json")
	if err != nil {
		return nil, err
	}
	sort.Strings(names)

	var all []*Place
	var problems []string
	slugs := map[string]string{}   // slug → файл
	osmRefs := map[string]string{} // osm_ref → slug
	for _, name := range names {
		if strings.HasPrefix(path.Base(name), "candidates") {
			continue
		}
		data, err := fs.ReadFile(fsys, name)
		if err != nil {
			return nil, err
		}
		dec := json.NewDecoder(bytes.NewReader(data))
		dec.DisallowUnknownFields() // опечатка в имени поля — ошибка, а не тихий пропуск
		var list []*Place
		if err := dec.Decode(&list); err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		for i, p := range list {
			p.Normalize()
			where := fmt.Sprintf("%s[%d] %s", name, i, p.Slug)
			for field, msg := range p.Validate() {
				problems = append(problems, fmt.Sprintf("%s: %s: %s", where, field, msg))
			}
			if prev, dup := slugs[p.Slug]; dup && p.Slug != "" {
				problems = append(problems, fmt.Sprintf("%s: slug уже есть в %s", where, prev))
			}
			slugs[p.Slug] = name
			if p.OSMRef != "" {
				if prev, dup := osmRefs[p.OSMRef]; dup {
					problems = append(problems, fmt.Sprintf("%s: osm_ref %s уже у %s", where, p.OSMRef, prev))
				}
				osmRefs[p.OSMRef] = p.Slug
			}
			all = append(all, p)
		}
	}
	if len(problems) > 0 {
		sort.Strings(problems)
		return nil, fmt.Errorf("%w:\n  %s", ErrInvalid, strings.Join(problems, "\n  "))
	}
	return all, nil
}

// SeedStats — итог Seed.
type SeedStats struct{ Inserted, Updated, Unchanged int }

func (s SeedStats) String() string {
	return fmt.Sprintf("добавлено %d, обновлено %d, без изменений %d", s.Inserted, s.Updated, s.Unchanged)
}

// Seed записывает места одной транзакцией: либо все, либо ничего.
func (s *Service) Seed(ctx context.Context, list []*Place) (SeedStats, error) {
	var st SeedStats
	err := pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		st = SeedStats{}
		for _, p := range list {
			res, err := upsert(ctx, tx, p)
			if err != nil {
				return err
			}
			switch res {
			case Inserted:
				st.Inserted++
			case Updated:
				st.Updated++
			default:
				st.Unchanged++
			}
		}
		return nil
	})
	if err != nil {
		return SeedStats{}, err // транзакция откатилась — ничего не записано
	}
	return st, nil
}
