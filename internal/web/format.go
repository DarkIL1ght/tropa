package web

import (
	"strings"

	"tropa/internal/meetups"
)

// when и clock — время по Алматы, общие с текстами уведомлений.
var (
	when  = meetups.When
	clock = meetups.Clock
)

// initial — первая буква имени для аватара-кружка.
func initial(name string) string {
	for _, r := range strings.TrimSpace(name) {
		return strings.ToUpper(string(r))
	}
	return "?"
}

// plural выбирает форму слова по числу: plural(n, "день", "дня", "дней").
func plural(n int, one, few, many string) string {
	n10, n100 := n%10, n%100
	switch {
	case n10 == 1 && n100 != 11:
		return one
	case n10 >= 2 && n10 <= 4 && (n100 < 12 || n100 > 14):
		return few
	default:
		return many
	}
}
