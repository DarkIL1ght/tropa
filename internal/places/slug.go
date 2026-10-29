package places

import (
	"strings"
	"unicode"
)

// translit — кириллица (русский и казахский) → латиница для slug.
var translit = map[rune]string{
	'а': "a", 'б': "b", 'в': "v", 'г': "g", 'д': "d", 'е': "e", 'ё': "e", 'ж': "zh", 'з': "z",
	'и': "i", 'й': "y", 'к': "k", 'л': "l", 'м': "m", 'н': "n", 'о': "o", 'п': "p", 'р': "r",
	'с': "s", 'т': "t", 'у': "u", 'ф': "f", 'х': "kh", 'ц': "ts", 'ч': "ch", 'ш': "sh", 'щ': "sch",
	'ъ': "", 'ы': "y", 'ь': "", 'э': "e", 'ю': "yu", 'я': "ya",
	'ә': "a", 'ғ': "g", 'қ': "k", 'ң': "n", 'ө': "o", 'ұ': "u", 'ү': "u", 'һ': "h", 'і': "i",
}

// Slugify — «Парк имени 28 панфиловцев» → «park-imeni-28-panfilovtsev».
func Slugify(s string) string {
	var b strings.Builder
	dash := false
	for _, c := range strings.ToLower(s) {
		var part string
		switch {
		case c >= 'a' && c <= 'z' || c >= '0' && c <= '9':
			part = string(c)
		case translit[c] != "" || c == 'ъ' || c == 'ь':
			part = translit[c]
		default:
			if unicode.IsLetter(c) || unicode.IsDigit(c) {
				continue // прочие алфавиты пропускаем
			}
			if b.Len() > 0 {
				dash = true
			}
			continue
		}
		if part == "" {
			continue
		}
		if dash {
			b.WriteByte('-')
			dash = false
		}
		b.WriteString(part)
	}
	out := b.String()
	if len(out) > 70 {
		out = strings.TrimRight(out[:70], "-")
	}
	return out
}
