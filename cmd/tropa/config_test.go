package main

import "testing"

func TestLoadConfig(t *testing.T) {
	env := map[string]string{"SITE_URL": "https://example.kz/", "EMAIL_VERIFICATION": "1", "TELEGRAM_API_URL": "http://bot.local/"}
	c := loadConfig(func(k string) string { return env[k] })
	if c.Addr != "127.0.0.1:8080" || c.SiteURL != "https://example.kz" || !c.RequireEmail || c.SecureCookies ||
		c.TelegramAPI != "http://bot.local" || c.MailFrom == "" {
		t.Fatalf("настройки: %+v", c)
	}
}
