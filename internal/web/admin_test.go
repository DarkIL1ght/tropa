package web

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
)

var adminResetRe = regexp.MustCompile(`value="https://tropa\.test(/password/reset/[A-Za-z0-9_-]+)"`)

func TestAdmin(t *testing.T) {
	ts, db := newTestEnv(t, func(s *Server) { s.cfg.SiteURL = "https://tropa.test" })
	ctx := context.Background()
	boss := signup(t, ts.URL, "boss@tropa.kz")
	user := signup(t, ts.URL, "user@tropa.kz")
	if _, err := db.Exec(ctx, `UPDATE users SET role = 'admin' WHERE email = 'boss@tropa.kz'`); err != nil {
		t.Fatal(err)
	}
	var userID, bossID string
	db.QueryRow(ctx, `SELECT id::text FROM users WHERE email = 'user@tropa.kz'`).Scan(&userID)
	db.QueryRow(ctx, `SELECT id::text FROM users WHERE email = 'boss@tropa.kz'`).Scan(&bossID)

	// не-админу и гостю админки как будто нет
	if code, _ := user.get("/admin"); code != http.StatusNotFound {
		t.Fatalf("пользователь в админке: %d", code)
	}
	if resp, _ := newClient(t, ts.URL).http.Get(ts.URL + "/admin"); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("гость в админке: %d", resp.StatusCode)
	}
	if _, body := user.get("/"); strings.Contains(body, `href="/admin"`) {
		t.Fatal("ссылка на админку видна не-админу")
	}

	code, body := boss.get("/admin")
	if code != 200 || !strings.Contains(body, "user@tropa.kz") || !strings.Contains(body, "Пользователей: <strong>2</strong>") {
		t.Fatalf("список пользователей: %d", code)
	}
	if _, body := boss.get("/admin?q=boss"); strings.Contains(body, "user@tropa.kz") {
		t.Fatal("поиск не фильтрует")
	}
	if code, _ := boss.get("/admin/meetups"); code != 200 {
		t.Fatalf("встречи: %d", code)
	}

	// ссылка сброса из админки работает у пользователя
	_, page := boss.get("/admin")
	resp, err := boss.http.PostForm(ts.URL+"/admin/users/"+userID+"/reset", url.Values{"csrf_token": {csrfRe.FindStringSubmatch(page)[1]}})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	m := adminResetRe.FindStringSubmatch(string(b))
	if resp.StatusCode != 200 || m == nil {
		t.Fatalf("ссылка сброса: %d", resp.StatusCode)
	}
	if code, body := newClient(t, ts.URL).get(m[1]); code != 200 || !strings.Contains(body, "Пользователь user@tropa.kz") {
		t.Fatalf("ссылка из админки не открывается: %d", code)
	}

	// роли: назначить, снять; последнего админа снять нельзя
	if resp := boss.post("/admin", "/admin/users/"+userID+"/role", url.Values{"role": {"admin"}}); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("назначить админа: %d", resp.StatusCode)
	}
	if code, _ := user.get("/admin"); code != 200 {
		t.Fatalf("новый админ не попал в админку: %d", code)
	}
	if resp := boss.post("/admin", "/admin/users/"+userID+"/role", url.Values{"role": {"user"}}); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("снять админа: %d", resp.StatusCode)
	}
	if resp := boss.post("/admin", "/admin/users/"+bossID+"/role", url.Values{"role": {"user"}}); resp.StatusCode != http.StatusConflict {
		t.Fatalf("снятие последнего админа: %d, ждали 409", resp.StatusCode)
	}
	if code, _ := user.get("/admin"); code != http.StatusNotFound {
		t.Fatalf("снятый админ остался в админке: %d", code)
	}
}
