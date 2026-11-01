package mail

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Resend отправляет письма через API resend.com. Без своего проверенного
// домена Resend отправляет только на адрес владельца аккаунта (onboarding@resend.dev).
type Resend struct {
	Key  string
	From string // «Tropa <noreply@домен>»
	// URL — адрес API (подменяется в тестах).
	URL    string
	Client *http.Client
}

func NewResend(key, from string) *Resend {
	return &Resend{Key: key, From: from, URL: "https://api.resend.com/emails", Client: &http.Client{Timeout: 10 * time.Second}}
}

func (r *Resend) Send(ctx context.Context, to, subject, body string) error {
	payload, _ := json.Marshal(map[string]any{"from": r.From, "to": []string{to}, "subject": subject, "text": body})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.URL, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+r.Key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := r.Client.Do(req)
	if err != nil {
		return fmt.Errorf("resend: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		// в тексте ошибки Resend нет ключа, только причина (домен не проверен, лимит и т.п.)
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 500))
		return fmt.Errorf("resend: %d %s", resp.StatusCode, bytes.TrimSpace(msg))
	}
	return nil
}
