package web

import (
	"fmt"
	"strconv"
	"time"
)

var monthsGen = [...]string{"января", "февраля", "марта", "апреля", "мая", "июня",
	"июля", "августа", "сентября", "октября", "ноября", "декабря"}

func dayMonth(t time.Time) string { return fmt.Sprintf("%d %s", t.Day(), monthsGen[t.Month()-1]) }

// dateRange — «12–14 марта 2027», «30 марта – 2 апреля 2027», «5 мая 2027».
func dateRange(start, end time.Time) string {
	switch {
	case start.Equal(end):
		return fmt.Sprintf("%s %d", dayMonth(start), start.Year())
	case start.Year() != end.Year():
		return fmt.Sprintf("%s %d – %s %d", dayMonth(start), start.Year(), dayMonth(end), end.Year())
	case start.Month() != end.Month():
		return fmt.Sprintf("%s – %s %d", dayMonth(start), dayMonth(end), end.Year())
	default:
		return fmt.Sprintf("%d–%d %s %d", start.Day(), end.Day(), monthsGen[end.Month()-1], end.Year())
	}
}

// money — «15 000 ₸» с неразрывными пробелами между разрядами, «бесплатно» для 0.
func money(n int) string {
	if n == 0 {
		return "бесплатно"
	}
	s := strconv.Itoa(n)
	out := make([]byte, 0, len(s)+len(s)/3*2+4)
	for i := range len(s) {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, " "...)
		}
		out = append(out, s[i])
	}
	return string(out) + " ₸"
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
