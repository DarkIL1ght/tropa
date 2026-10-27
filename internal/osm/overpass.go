package osm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"tropa/internal/places"
)

// Endpoints — публичные серверы Overpass; при ошибке пробуем следующий.
var Endpoints = []string{
	"https://overpass-api.de/api/interpreter",
	"https://overpass.private.coffee/api/interpreter",
	"https://overpass.kumi.systems/api/interpreter",
}

// UserAgent — по правилам Overpass и OSM: название и ссылка на проект,
// без личных контактов.
const UserAgent = "tropa/0.1 (+https://github.com/DarkIL1ght/Invication)"

// selectors — теги, которые превращаются в места (см. Classify).
var selectors = []string{
	`nwr["leisure"~"^(park|ice_rink|bowling_alley|trampoline_park|escape_game|nature_reserve|ski_resort)$"]["name"]`,
	`nwr["leisure"="garden"]["name"]`,
	`nwr["leisure"~"^(sports_centre|fitness_centre)$"]["sport"~"climbing|trampoline|10pin"]["name"]`,
	`nwr["sport"="karting"]["name"]`,
	`nwr["landuse"="winter_sports"]["name"]`,
	`nwr["tourism"~"^(museum|gallery|viewpoint)$"]["name"]`,
	`nwr["amenity"~"^(theatre|cinema|arts_centre|karaoke_box)$"]["name"]`,
	`nwr["shop"="mall"]["name"]`,
	`nwr["amenity"~"^(cafe|bar|restaurant)$"]["name"~"антикафе|анти-кафе|тайм-?кафе|anti-?cafe|настольн|board ?game|караоке|karaoke|квест|quest",i]`,
	`way["highway"="pedestrian"]["name"]`,
	`node["natural"="peak"]["name"]`,
	`node["waterway"="waterfall"]["name"]`,
	`nwr["natural"~"^(valley|gorge)$"]["name"]`,
	`nwr["natural"="water"]["water"~"^(lake|reservoir)$"]["name"]`,
}

// Query — запрос Overpass QL по рамке: всё в одном ответе, с рамками
// объектов (out bb), чтобы посчитать центр и размер.
func Query(b places.BBox) string {
	box := fmt.Sprintf("(%.4f,%.4f,%.4f,%.4f)", b.South, b.West, b.North, b.East)
	var sb strings.Builder
	sb.WriteString("[out:json][timeout:180];\n(\n")
	for _, s := range selectors {
		sb.WriteString("  " + s + box + ";\n")
	}
	sb.WriteString(");\nout tags bb;\n")
	return sb.String()
}

// Client ходит в Overpass. Пустые поля — значения по умолчанию.
type Client struct {
	HTTP      *http.Client
	Endpoints []string
	Retries   int           // попыток на каждый сервер
	Backoff   time.Duration // пауза между попытками (растёт вдвое)
}

type response struct {
	Elements []Element `json:"elements"`
	Remark   string    `json:"remark"` // Overpass пишет сюда о таймаутах внутри «успешного» ответа
}

// Fetch выполняет запрос, перебирая серверы. 429 и 5xx — повод
// подождать и попробовать ещё; 4xx остальные — ошибка в запросе.
func (c *Client) Fetch(ctx context.Context, query string) ([]Element, error) {
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 4 * time.Minute}
	}
	eps := c.Endpoints
	if len(eps) == 0 {
		eps = Endpoints
	}
	retries := max(c.Retries, 1)
	backoff := c.Backoff
	if backoff == 0 {
		backoff = 5 * time.Second
	}

	var errs []error
	for _, ep := range eps {
		wait := backoff
		for attempt := 1; attempt <= retries; attempt++ {
			els, retry, err := c.once(ctx, hc, ep, query)
			if err == nil {
				return els, nil
			}
			errs = append(errs, fmt.Errorf("%s (попытка %d): %w", ep, attempt, err))
			if !retry || ctx.Err() != nil {
				break
			}
			if attempt < retries {
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-time.After(wait):
				}
				wait *= 2
			}
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}
	return nil, errors.Join(errs...)
}

func (c *Client) once(ctx context.Context, hc *http.Client, ep, query string) (els []Element, retry bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ep,
		strings.NewReader(url.Values{"data": {query}}.Encode()))
	if err != nil {
		return nil, false, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", UserAgent)
	resp, err := hc.Do(req)
	if err != nil {
		return nil, true, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
		return nil, true, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, false, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	var r response
	if err := json.NewDecoder(io.LimitReader(resp.Body, 256<<20)).Decode(&r); err != nil {
		return nil, true, fmt.Errorf("ответ не JSON: %w", err)
	}
	if strings.Contains(r.Remark, "runtime error") {
		return nil, true, fmt.Errorf("Overpass: %s", r.Remark)
	}
	return r.Elements, false, nil
}
