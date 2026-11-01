package mail

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestResend(t *testing.T) {
	var got map[string]any
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		json.NewDecoder(r.Body).Decode(&got)
		if got["subject"] == "fail" {
			w.WriteHeader(http.StatusForbidden)
			w.Write([]byte(`{"message":"domain is not verified"}`))
			return
		}
		w.Write([]byte(`{"id":"1"}`))
	}))
	defer srv.Close()
	r := NewResend("re_test", "Tropa <noreply@tropa.test>")
	r.URL = srv.URL
	if err := r.Send(context.Background(), "a@b.kz", "Код", "123456"); err != nil {
		t.Fatal(err)
	}
	if auth != "Bearer re_test" || got["from"] != "Tropa <noreply@tropa.test>" || got["text"] != "123456" {
		t.Fatalf("запрос: %s %v", auth, got)
	}
	if to, _ := got["to"].([]any); len(to) != 1 || to[0] != "a@b.kz" {
		t.Fatalf("получатель: %v", got["to"])
	}
	err := r.Send(context.Background(), "a@b.kz", "fail", "x")
	if err == nil || !strings.Contains(err.Error(), "domain is not verified") || strings.Contains(err.Error(), "re_test") {
		t.Fatalf("ошибка: %v", err)
	}
}
