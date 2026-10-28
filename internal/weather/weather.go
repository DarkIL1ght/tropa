// Package weather — текущая погода в Алматы из Open-Meteo (бесплатно, без ключа).
//
// Погода — украшение: при любой ошибке Current отдаёт ошибку, а страница
// просто показывается без плашки. Чтобы не ждать упавший сервис на каждом
// запросе, неудача тоже запоминается — на минуту.
package weather

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"sync"
	"time"
)

// DefaultBaseURL — адрес API прогноза Open-Meteo.
const DefaultBaseURL = "https://api.open-meteo.com/v1/forecast"

const (
	cacheTTL   = 30 * time.Minute
	failTTL    = time.Minute
	reqTimeout = 3 * time.Second
	// StrongWind — с какой скорости ветра (км/ч, ≈ 10 м/с) гулять неуютно.
	StrongWind = 36.0
)

// Weather — погода на сейчас.
type Weather struct {
	TempC     float64 // температура воздуха, °C
	Code      int     // код погоды WMO
	WindKmh   float64 // ветер на 10 м, км/ч
	PrecipMM  float64 // осадки за последний интервал, мм
	FetchedAt time.Time
}

// Precipitation — идут ли осадки: по коду WMO (морось, дождь, снег, гроза)
// или по измеренному количеству.
func (w Weather) Precipitation() bool {
	return (w.Code >= 51 && w.Code <= 99) || w.PrecipMM > 0
}

// Cold — повод искать место в тепле: мороз, осадки или сильный ветер.
func (w Weather) Cold() bool {
	return w.TempC <= 0 || w.Precipitation() || w.WindKmh >= StrongWind
}

// Temp — температура для подписи: «−8 °C», «+3 °C», «0 °C».
func (w Weather) Temp() string {
	t := int(math.Round(w.TempC))
	switch {
	case t > 0:
		return fmt.Sprintf("+%d °C", t)
	case t < 0:
		return fmt.Sprintf("−%d °C", -t) // настоящий минус, не дефис
	}
	return "0 °C"
}

// Label — погода словами, по коду WMO.
func (w Weather) Label() string {
	switch c := w.Code; {
	case c == 0:
		return "ясно"
	case c <= 2:
		return "переменная облачность"
	case c == 3:
		return "пасмурно"
	case c == 45 || c == 48:
		return "туман"
	case c >= 51 && c <= 57:
		return "морось"
	case c >= 61 && c <= 67, c >= 80 && c <= 82:
		return "дождь"
	case c >= 71 && c <= 77, c == 85 || c == 86:
		return "снег"
	case c >= 95 && c <= 99:
		return "гроза"
	}
	return ""
}

// Summary — короткая сводка для плашки: «−8 °C, снег», «+5 °C, ясно, сильный ветер».
func (w Weather) Summary() string {
	s := w.Temp()
	if l := w.Label(); l != "" {
		s += ", " + l
	}
	if w.WindKmh >= StrongWind {
		s += ", сильный ветер"
	}
	return s
}

// Client ходит в Open-Meteo и кэширует ответ. Безопасен для горутин.
type Client struct {
	BaseURL  string
	Lat, Lon float64
	HTTP     *http.Client
	Now      func() time.Time // подменяется в тестах

	mu      sync.Mutex
	cached  *Weather
	failed  time.Time // когда был последний неудачный запрос
	lastErr error
}

// New — клиент для точки lat, lon с настройками по умолчанию.
func New(lat, lon float64) *Client {
	return &Client{BaseURL: DefaultBaseURL, Lat: lat, Lon: lon, HTTP: http.DefaultClient, Now: time.Now}
}

// Current — погода сейчас: из кэша (до 30 минут) или свежая.
// Запросы идут по одному: пока один ждёт Open-Meteo, остальные ждут его.
func (c *Client) Current(ctx context.Context) (Weather, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.Now()
	if c.cached != nil && now.Sub(c.cached.FetchedAt) < cacheTTL {
		return *c.cached, nil
	}
	if !c.failed.IsZero() && now.Sub(c.failed) < failTTL {
		return Weather{}, c.lastErr
	}
	w, err := c.fetch(ctx)
	if err != nil {
		c.failed, c.lastErr = now, err
		return Weather{}, err
	}
	w.FetchedAt = now
	c.cached, c.failed = &w, time.Time{}
	return w, nil
}

type apiResponse struct {
	Current *struct {
		Temperature   *float64 `json:"temperature_2m"`
		Precipitation float64  `json:"precipitation"`
		WeatherCode   int      `json:"weather_code"`
		WindSpeed     float64  `json:"wind_speed_10m"`
	} `json:"current"`
}

func (c *Client) fetch(ctx context.Context) (Weather, error) {
	ctx, cancel := context.WithTimeout(ctx, reqTimeout)
	defer cancel()
	q := url.Values{}
	q.Set("latitude", fmt.Sprintf("%.4f", c.Lat))
	q.Set("longitude", fmt.Sprintf("%.4f", c.Lon))
	q.Set("current", "temperature_2m,precipitation,weather_code,wind_speed_10m")
	q.Set("timezone", "Asia/Almaty")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"?"+q.Encode(), nil)
	if err != nil {
		return Weather{}, err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return Weather{}, fmt.Errorf("open-meteo: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Weather{}, fmt.Errorf("open-meteo: HTTP %d", resp.StatusCode)
	}
	var r apiResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&r); err != nil {
		return Weather{}, fmt.Errorf("open-meteo: %w", err)
	}
	if r.Current == nil || r.Current.Temperature == nil {
		return Weather{}, errors.New("open-meteo: в ответе нет текущей погоды")
	}
	return Weather{
		TempC: *r.Current.Temperature, Code: r.Current.WeatherCode,
		WindKmh: r.Current.WindSpeed, PrecipMM: r.Current.Precipitation,
	}, nil
}
