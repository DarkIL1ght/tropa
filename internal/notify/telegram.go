package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// DefaultAPIURL — адрес Telegram Bot API.
const DefaultAPIURL = "https://api.telegram.org"

// Bot — минимальный клиент Telegram Bot API на net/http: только то, что нужно сайту.
type Bot struct {
	Token   string
	BaseURL string
	HTTP    *http.Client
}

func NewBot(token string) *Bot {
	return &Bot{Token: token, BaseURL: DefaultAPIURL, HTTP: &http.Client{Timeout: 40 * time.Second}}
}

// APIError — ответ Telegram с ok=false.
type APIError struct {
	Code        int
	Description string
	RetryAfter  time.Duration // для 429: сколько подождать
}

func (e *APIError) Error() string { return fmt.Sprintf("telegram: %d %s", e.Code, e.Description) }

// Blocked — пользователь заблокировал бота или чата больше нет: слать бесполезно.
func (e *APIError) Blocked() bool { return e.Code == http.StatusForbidden }

// Permanent — повтор не поможет (кроме 429 и ошибок сервера Telegram).
func (e *APIError) Permanent() bool {
	return e.Code != http.StatusTooManyRequests && e.Code < 500
}

func (b *Bot) call(ctx context.Context, method string, params, result any) error {
	body, err := json.Marshal(params)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.BaseURL+"/bot"+b.Token+"/"+method, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := b.HTTP.Do(req)
	if err != nil {
		// в тексте ошибки net/http есть URL с токеном — не пишем его в логи
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("telegram %s: сетевая ошибка", method)
	}
	defer resp.Body.Close()
	var r struct {
		OK          bool            `json:"ok"`
		Result      json.RawMessage `json:"result"`
		ErrorCode   int             `json:"error_code"`
		Description string          `json:"description"`
		Parameters  struct {
			RetryAfter int `json:"retry_after"`
		} `json:"parameters"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&r); err != nil {
		return &APIError{Code: resp.StatusCode, Description: "ответ не JSON"}
	}
	if !r.OK {
		code := r.ErrorCode
		if code == 0 {
			code = resp.StatusCode
		}
		return &APIError{Code: code, Description: r.Description, RetryAfter: time.Duration(r.Parameters.RetryAfter) * time.Second}
	}
	if result != nil {
		return json.Unmarshal(r.Result, result)
	}
	return nil
}

// Username — имя бота для ссылок t.me/<имя>.
func (b *Bot) Username(ctx context.Context) (string, error) {
	var me struct {
		Username string `json:"username"`
	}
	err := b.call(ctx, "getMe", struct{}{}, &me)
	return me.Username, err
}

// Send — простое текстовое сообщение без разметки: в тексте названия
// встреч от пользователей, экранировать их под Markdown незачем.
func (b *Bot) Send(ctx context.Context, chatID int64, text string) error {
	return b.call(ctx, "sendMessage", map[string]any{
		"chat_id": chatID, "text": text, "link_preview_options": map[string]bool{"is_disabled": true},
	}, nil)
}

// Update — входящее сообщение боту (остальные виды апдейтов не запрашиваем).
type Update struct {
	ID      int64 `json:"update_id"`
	Message *struct {
		Text string `json:"text"`
		Chat struct {
			ID   int64  `json:"id"`
			Type string `json:"type"`
		} `json:"chat"`
	} `json:"message"`
}

// Updates — long polling: ждёт новые сообщения до timeout.
func (b *Bot) Updates(ctx context.Context, offset int64, timeout time.Duration) ([]Update, error) {
	var ups []Update
	err := b.call(ctx, "getUpdates", map[string]any{
		"offset": offset, "timeout": int(timeout.Seconds()), "allowed_updates": []string{"message"},
	}, &ups)
	return ups, err
}
