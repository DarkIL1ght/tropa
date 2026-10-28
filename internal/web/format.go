package web

import (
	"strings"
	"time"

	"tropa/internal/meetups"
)

// when и clock — время по Алматы, общие с текстами уведомлений.
var (
	when  = meetups.When
	clock = meetups.Clock
)

var (
	monthsShort   = [...]string{"янв", "фев", "мар", "апр", "мая", "июн", "июл", "авг", "сен", "окт", "ноя", "дек"}
	weekdaysShort = [...]string{"вс", "пн", "вт", "ср", "чт", "пт", "сб"}
)

// stub — части даты для «корешка» билета встречи, по времени Алматы.
type stub struct{ Day, Month, Weekday, Time string }

func ticket(t time.Time) stub {
	t = t.In(meetups.Almaty)
	return stub{Day: t.Format("2"), Month: monthsShort[t.Month()-1], Weekday: weekdaysShort[t.Weekday()], Time: t.Format("15:04")}
}

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
