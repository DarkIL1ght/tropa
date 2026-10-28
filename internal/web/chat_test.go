package web

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"tropa/internal/meetups"
	"tropa/internal/places"
)

func TestChatFlow(t *testing.T) {
	ts, db := newTestEnv(t)
	seedPlaces(t, places.New(db), &places.Place{
		Slug: "test-rink", Name: "Тестовый каток", Category: places.Active, Kind: "rink",
		Lat: 43.22, Lon: 76.93, Area: places.Almaty, Season: "winter",
	})
	org := signup(t, ts.URL, "org@tropa.kz")
	friend := signup(t, ts.URL, "friend@tropa.kz")
	stranger := signup(t, ts.URL, "stranger@tropa.kz")
	loc := org.post("/places/test-rink/meetups/new", "/places/test-rink/meetups", meetupForm("Каток", "", "friends")).Header.Get("Location")
	if resp := friend.post(loc, loc+"/join", url.Values{}); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("запись: %d", resp.StatusCode)
	}

	// посторонний видит приглашение взять билет, но не чат
	if _, body := stranger.get(loc); strings.Contains(body, `id="chat"`) || !strings.Contains(body, "Чат встречи — для участников") {
		t.Fatal("посторонний видит чат")
	}
	if code, _ := stranger.get(loc + "/stream"); code != http.StatusForbidden {
		t.Fatalf("поток постороннему: %d", code)
	}
	if resp := stranger.post(loc, loc+"/messages", url.Values{"body": {"спам"}}); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("сообщение постороннего: %d", resp.StatusCode)
	}

	// участник открывает поток и получает сообщение организатора
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", ts.URL+loc+"/stream?after=0", nil)
	resp, err := friend.http.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("поток: %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	lines := bufio.NewScanner(resp.Body)
	waitFor := func(prefix string) string {
		t.Helper()
		for lines.Scan() {
			if l := lines.Text(); strings.HasPrefix(l, prefix) {
				return strings.TrimPrefix(l, prefix)
			}
		}
		t.Fatalf("поток закончился, не дождались %q: %v", prefix, lines.Err())
		return ""
	}
	waitFor(": ok") // подписка готова

	if resp := org.post(loc, loc+"/messages", url.Values{"body": {`Беру <b>термос</b>`}}); resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != loc+"#chat" {
		t.Fatalf("отправка формой: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	var ev chatEvent
	if err := json.Unmarshal([]byte(waitFor("data: ")), &ev); err != nil || ev.Body != `Беру <b>термос</b>` || ev.Name != "Пользователь org@tropa.kz" {
		t.Fatalf("событие: %+v %v", ev, err)
	}

	// история на странице, текст экранирован
	_, body := friend.get(loc)
	if !strings.Contains(body, `id="chat"`) || !strings.Contains(body, "Беру &lt;b&gt;термос&lt;/b&gt;") {
		t.Fatal("нет истории чата или текст не экранирован")
	}

	// отправка из chat.js: 204 без редиректа, ошибки — JSON
	_, page := friend.get(loc)
	form := url.Values{"body": {"Буду в 10"}, "csrf_token": {csrfRe.FindStringSubmatch(page)[1]}}
	jsPost := func(f url.Values) *http.Response {
		req, _ := http.NewRequest("POST", ts.URL+loc+"/messages", strings.NewReader(f.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Accept", "application/json")
		resp, err := friend.http.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp
	}
	if resp := jsPost(form); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("отправка из JS: %d", resp.StatusCode)
	}
	if err := json.Unmarshal([]byte(waitFor("data: ")), &ev); err != nil || ev.Body != "Буду в 10" {
		t.Fatalf("своё сообщение в потоке: %+v %v", ev, err)
	}
	form.Set("body", " ")
	if resp := jsPost(form); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("пустое сообщение: %d", resp.StatusCode)
	}
}

func TestHubSlowSubscriber(t *testing.T) {
	h := newHub()
	ch, cancel := h.subscribe(1)
	defer cancel()
	for i := range 20 { // буфер 16: медленного подписчика отключаем, а не ждём
		h.publish(meetups.Message{MeetupID: 1, ID: int64(i)})
	}
	n := 0
	for range ch {
		n++
	}
	if n != 16 {
		t.Fatalf("получено %d до отключения", n)
	}
	h.close()
	if ch2, _ := h.subscribe(1); func() bool { _, ok := <-ch2; return ok }() {
		t.Fatal("после close подписка должна сразу закрываться")
	}
}
