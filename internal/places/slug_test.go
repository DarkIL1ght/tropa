package places

import (
	"strings"
	"testing"
)

func TestSlugify(t *testing.T) {
	for in, want := range map[string]string{
		"Парк имени 28 гвардейцев-панфиловцев": "park-imeni-28-gvardeytsev-panfilovtsev",
		"Көлсай көлдері":                       "kolsay-kolderi",
		`ТРЦ "MART Village"`:                   "trts-mart-village",
		"Lumièra":                              "lumira",
		"  Бұлақ 💦 ":                           "bulak",
		"Щучье, ёлка":                          "schuche-elka",
		"!!!":                                  "",
	} {
		if got := Slugify(in); got != want {
			t.Errorf("Slugify(%q) = %q, ждали %q", in, got, want)
		}
	}
	if long := Slugify(strings.Repeat("очень длинное название ", 10)); len(long) > 70 || strings.HasSuffix(long, "-") {
		t.Errorf("длинный slug: %q", long)
	}
}
