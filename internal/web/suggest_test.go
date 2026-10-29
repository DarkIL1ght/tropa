package web

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func suggestForm(name string) url.Values {
	return url.Values{
		"name": {name}, "kind": {"indoor:anticafe"}, "coords": {"43.2389, 76.9455"},
		"address": {"Абая 10"}, "description": {"Настолки и чай"}, "season": {"all_year"}, "indoor": {"1"},
	}
}

func TestSuggestPlace(t *testing.T) {
	ts, db := newTestEnv(t)
	ctx := context.Background()
	user := signup(t, ts.URL, "user@tropa.kz")
	boss := signup(t, ts.URL, "boss@tropa.kz")
	if _, err := db.Exec(ctx, `UPDATE users SET role = 'admin' WHERE email = 'boss@tropa.kz'`); err != nil {
		t.Fatal(err)
	}

	// гостя — на вход, не-админа в модерацию не пускаем
	if resp, _ := newClient(t, ts.URL).http.Get(ts.URL + "/places/suggest"); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("гость на форме: %d", resp.StatusCode)
	}
	if code, _ := user.get("/admin/places"); code != http.StatusNotFound {
		t.Fatalf("пользователь в модерации: %d", code)
	}
	if _, body := user.get("/"); !strings.Contains(body, `href="/places/suggest"`) {
		t.Fatal("на карте нет ссылки «Предложить»")
	}

	// ошибка: точка за пределами области — форма с ошибкой и введёнными данными
	bad := suggestForm("Где-то в Астане")
	bad.Set("coords", "51.13, 71.43")
	if resp := user.post("/places/suggest", "/places/suggest", bad); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("точка вне области: %d", resp.StatusCode)
	}

	if resp := user.post("/places/suggest", "/places/suggest", suggestForm("Антикафе Время")); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("предложение: %d", resp.StatusCode)
	}
	if resp := user.post("/places/suggest", "/places/suggest", suggestForm("Квест Дубль")); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("второе предложение: %d", resp.StatusCode)
	}
	if _, body := user.get("/places/suggest"); !strings.Contains(body, "Антикафе Время") || !strings.Contains(body, "На проверке") {
		t.Fatal("своих предложений не видно")
	}
	if code, _ := user.get("/places/antikafe-vremya"); code != http.StatusNotFound {
		t.Fatalf("черновик открыт на сайте: %d", code)
	}

	var first, second string
	db.QueryRow(ctx, `SELECT id::text FROM places WHERE slug = 'antikafe-vremya'`).Scan(&first)
	db.QueryRow(ctx, `SELECT id::text FROM places WHERE slug = 'kvest-dubl'`).Scan(&second)

	code, body := boss.get("/admin/places")
	if code != 200 || !strings.Contains(body, "Антикафе Время") || !strings.Contains(body, "Места · 2") {
		t.Fatalf("очередь модерации: %d", code)
	}

	// правка перед публикацией
	if code, body := boss.get("/admin/places/" + first + "/edit"); code != 200 || !strings.Contains(body, `value="43.238900, 76.945500"`) {
		t.Fatalf("форма правки: %d", code)
	}
	edit := suggestForm("Антикафе «Время»")
	edit.Set("edit", "1")
	if resp := boss.post("/admin/places/"+first+"/edit", "/admin/places/"+first+"/publish", edit); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("публикация: %d", resp.StatusCode)
	}
	if code, body := user.get("/places/antikafe-vremya"); code != 200 || !strings.Contains(body, "Антикафе «Время»") {
		t.Fatalf("опубликованное место: %d", code)
	}

	// отказ без причины не проходит, с причиной — автор её видит
	boss.post("/admin/places", "/admin/places/"+second+"/reject", url.Values{"reason": {""}})
	if _, body := boss.get("/admin/places"); !strings.Contains(body, "Квест Дубль") {
		t.Fatal("отказ без причины сработал")
	}
	boss.post("/admin/places", "/admin/places/"+second+"/reject", url.Values{"reason": {"Такое место уже есть"}})
	if _, body := user.get("/places/suggest"); !strings.Contains(body, "Причина: Такое место уже есть") {
		t.Fatal("автор не видит причину отказа")
	}
	// повторное решение по проверенному месту — просто возврат к очереди
	if resp := boss.post("/admin/places", "/admin/places/"+second+"/publish", url.Values{}); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("повторное решение: %d", resp.StatusCode)
	}
	if code, _ := user.get("/places/kvest-dubl"); code != http.StatusNotFound {
		t.Fatalf("отклонённое опубликовано: %d", code)
	}
	if resp, _ := boss.http.Get(ts.URL + "/admin/places/" + second + "/edit"); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("правка проверенного: %d", resp.StatusCode)
	}
}

func TestParseCoords(t *testing.T) {
	for in, ok := range map[string]bool{
		"43.2389, 76.9455": true, "43.2389 76.9455": true, "43.2389;76.9455": true,
		"": false, "43.2389": false, "a, b": false, "1, 2, 3": false,
	} {
		if _, _, got := parseCoords(in); got != ok {
			t.Errorf("parseCoords(%q) = %v", in, got)
		}
	}
}
