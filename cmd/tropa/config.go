package main

import "strings"

// config — настройки из окружения (или .env, см. шапку main.go). Окружение читается только здесь:
// пакеты internal/ получают уже готовые значения.
type config struct {
	DatabaseURL   string
	Addr          string // адрес сервера, по умолчанию 127.0.0.1:8080
	SiteURL       string // без «/» в конце; пусто — serve подставит http://Addr
	SecureCookies bool
	TrustProxy    bool
	WeatherURL    string // пусто — Open-Meteo, off — без погоды
	TelegramToken string
	TelegramAPI   string // пусто — https://api.telegram.org
	ResendKey     string
	MailFrom      string
	RequireEmail  bool // EMAIL_VERIFICATION=1
}

func loadConfig(getenv func(string) string) config {
	c := config{
		DatabaseURL:   getenv("DATABASE_URL"),
		Addr:          getenv("ADDR"),
		SiteURL:       strings.TrimRight(getenv("SITE_URL"), "/"),
		SecureCookies: getenv("SECURE_COOKIES") == "1",
		TrustProxy:    getenv("TRUST_PROXY") == "1",
		WeatherURL:    getenv("WEATHER_URL"),
		TelegramToken: getenv("TELEGRAM_BOT_TOKEN"),
		TelegramAPI:   strings.TrimRight(getenv("TELEGRAM_API_URL"), "/"),
		ResendKey:     getenv("RESEND_API_KEY"),
		MailFrom:      getenv("MAIL_FROM"),
		RequireEmail:  getenv("EMAIL_VERIFICATION") == "1",
	}
	if c.Addr == "" {
		c.Addr = "127.0.0.1:8080"
	}
	if c.MailFrom == "" {
		c.MailFrom = "Tropa <onboarding@resend.dev>"
	}
	return c
}
