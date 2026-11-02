package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"
	"tropa/internal/auth"
	"tropa/internal/osm"
	"tropa/internal/places"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// resetLinkTTL — ссылка от админа живёт дольше, чем от бота: её передают вручную.
const resetLinkTTL = 24 * time.Hour

// resetLink печатает ссылку сброса пароля — админ передаёт её пользователю
// без Telegram (сам пароль админ не узнаёт).
func resetLink(ctx context.Context, log *slog.Logger, pool *pgxpool.Pool, site, email string) error {
	if err := migrate(ctx, log, pool); err != nil {
		return err
	}
	if site == "" {
		return errors.New("не задан SITE_URL: ссылка должна вести на сайт")
	}
	found, err := auth.New(pool).RequestReset(ctx, email, resetLinkTTL, func(_ pgx.Tx, _ int64, token string) error {
		fmt.Println(site + "/password/reset/" + token)
		return nil
	})
	if err == nil && !found {
		err = fmt.Errorf("пользователь %s не найден", email)
	}
	return err
}

// seed загружает места из папки (по умолчанию data/places в корне проекта).
// Сначала проверяются все файлы, потом всё пишется одной транзакцией.
func seed(ctx context.Context, log *slog.Logger, dbURL string, args []string) error {
	dir := ""
	if len(args) > 0 {
		dir = args[0]
	} else if wd, err := os.Getwd(); err == nil {
		dir = filepath.Join(moduleRoot(wd), "data", "places")
	}
	list, err := places.LoadSeed(os.DirFS(dir))
	if err != nil {
		return fmt.Errorf("%s: %w", dir, err)
	}
	return withDB(ctx, dbURL, func(pool *pgxpool.Pool) error {
		if err := migrate(ctx, log, pool); err != nil {
			return err
		}
		st, err := places.New(pool).Seed(ctx, list)
		if err != nil {
			return err
		}
		log.Info("места загружены", "dir", dir, "total", len(list), "result", st.String())
		return nil
	})
}

// osmFetch скачивает объекты OSM по области и пишет кандидатов, которых
// ещё нет в data/places. Файл кандидатов — для ручного разбора, seed его
// пропускает.
func osmFetch(ctx context.Context, log *slog.Logger, args []string) error {
	wd, err := os.Getwd()
	if err != nil {
		return err
	}
	dir := filepath.Join(moduleRoot(wd), "data", "places")
	existing, err := places.LoadSeed(os.DirFS(dir))
	if err != nil {
		return fmt.Errorf("%s: %w", dir, err)
	}

	cache := filepath.Join(os.TempDir(), "tropa-osm-raw.json")
	var elems []osm.Element
	switch {
	case len(args) == 2 && args[0] == "--from":
		data, err := os.ReadFile(args[1])
		if err != nil {
			return err
		}
		if err := json.Unmarshal(data, &elems); err != nil {
			return fmt.Errorf("%s: %w", args[1], err)
		}
		log.Info("объекты из файла", "path", args[1], "count", len(elems))
	case len(args) == 0:
		log.Info("запрос к Overpass — до нескольких минут")
		c := &osm.Client{Retries: 3}
		if elems, err = c.Fetch(ctx, osm.Query(places.RegionBBox)); err != nil {
			return err
		}
		raw, _ := json.Marshal(elems)
		if err := os.WriteFile(cache, raw, 0o644); err != nil {
			return err
		}
		log.Info("объекты OSM получены", "count", len(elems), "cache", cache)
	default:
		return errors.New("использование: tropa osm-fetch [--from raw.json]")
	}

	cands := osm.Candidates(elems, osm.NewExisting(existing))
	out := filepath.Join(dir, "candidates.json")
	if err := writePlacesJSON(out, cands); err != nil {
		return err
	}
	byKind := map[string]int{}
	for _, p := range cands {
		byKind[string(p.Category)+"/"+string(p.Kind)]++
	}
	log.Info("кандидаты записаны", "path", out, "count", len(cands), "by_kind", fmt.Sprint(byKind))
	return nil
}

// writePlacesJSON — JSON с отступами, кириллица без экранирования.
func writePlacesJSON(path string, list []*places.Place) error {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if list == nil {
		list = []*places.Place{}
	}
	if err := enc.Encode(list); err != nil {
		return err
	}
	return os.WriteFile(path, buf.Bytes(), 0o644)
}
