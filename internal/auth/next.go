package auth

import (
	"net/url"
	"strings"
)

// SafeNext возвращает локальный путь для редиректа после входа или "/".
// Отсекает «//evil.com» и «/\evil.com» — браузеры считают их внешними адресами.
func SafeNext(next string) string {
	if !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") || strings.HasPrefix(next, "/\\") {
		return "/"
	}
	if u, err := url.Parse(next); err != nil || u.Host != "" || u.Scheme != "" {
		return "/"
	}
	return next
}

func safeNext(next string) string { return url.QueryEscape(SafeNext(next)) }
