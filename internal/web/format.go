package web

import (
	"fmt"
	"time"

	"tropa/internal/meetups"
)

var monthsGen = [...]string{"января", "февраля", "марта", "апреля", "мая", "июня",
	"июля", "августа", "сентября", "октября", "ноября", "декабря"}

var weekdaysShort = [...]string{"вс", "пн", "вт", "ср", "чт", "пт", "сб"}

// when — время встречи по Алматы: «сб, 6 марта, 18:30»; год — только если не текущий.
func when(t time.Time) string {
	t = t.In(meetups.Almaty)
	s := fmt.Sprintf("%s, %d %s", weekdaysShort[t.Weekday()], t.Day(), monthsGen[t.Month()-1])
	if t.Year() != time.Now().In(meetups.Almaty).Year() {
		s += fmt.Sprintf(" %d", t.Year())
	}
	return s + ", " + t.Format("15:04")
}

// clock — «6 марта, 18:30» по Алматы: срок ответа на предложенное место.
func clock(t time.Time) string {
	t = t.In(meetups.Almaty)
	return fmt.Sprintf("%d %s, %s", t.Day(), monthsGen[t.Month()-1], t.Format("15:04"))
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
