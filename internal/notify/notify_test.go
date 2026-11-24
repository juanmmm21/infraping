package notify

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/juanmmm21/infraping/internal/config"
)

type captured struct {
	path    string
	headers http.Header
	body    []byte
}

func recorder(t *testing.T, status int) (*httptest.Server, *[]captured) {
	t.Helper()
	var mu sync.Mutex
	var got []captured
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
		}
		mu.Lock()
		got = append(got, captured{path: r.URL.Path, headers: r.Header.Clone(), body: body})
		mu.Unlock()
		w.WriteHeader(status)
		_, _ = w.Write([]byte("nope"))
	}))
	t.Cleanup(srv.Close)
	return srv, &got
}

var down = Message{
	Kind: KindDown, Check: "API", Target: "https://api.example.com", Reason: "timed out",
	At: time.Date(2026, 5, 1, 8, 30, 0, 0, time.UTC),
}

func TestMessageText(t *testing.T) {
	if !strings.Contains(down.Title(), "API is DOWN") {
		t.Errorf("title = %q", down.Title())
	}
	up := down
	up.Kind = KindRecovered
	up.Downtime = 3*time.Minute + 20*time.Second
	body := up.Body()
	for _, want := range []string{"Target: https://api.example.com", "Downtime: 3m 20s", "2026-05-01 08:30:00 UTC"} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q:\n%s", want, body)
		}
	}
	if formatDuration(2*time.Hour+5*time.Minute) != "2h 5m" || formatDuration(9*time.Second) != "9s" {
		t.Error("formatDuration")
	}
}

func TestTelegram(t *testing.T) {
	srv, got := recorder(t, http.StatusOK)
	n := &telegram{name: "tg", client: srv.Client(), apiURL: srv.URL, token: "T0K", chatID: "42"}
	if err := n.Send(context.Background(), down); err != nil {
		t.Fatalf("Send: %v", err)
	}
	c := (*got)[0]
	if c.path != "/botT0K/sendMessage" {
		t.Errorf("path = %s", c.path)
	}
	var payload map[string]any
	if err := json.Unmarshal(c.body, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["chat_id"] != "42" || !strings.Contains(payload["text"].(string), "timed out") {
		t.Errorf("payload = %v", payload)
	}
}

func TestDiscordSlackWebhook(t *testing.T) {
	srv, got := recorder(t, http.StatusNoContent)
	client := srv.Client()
	for _, cfg := range []config.NotifierConfig{
		{Name: "d", Type: config.NotifierDiscord, URL: srv.URL + "/discord"},
		{Name: "s", Type: config.NotifierSlack, URL: srv.URL + "/slack"},
		{Name: "w", Type: config.NotifierWebhook, URL: srv.URL + "/hook", Headers: map[string]string{"X-Key": "k"}},
	} {
		n, err := New(cfg, client)
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		if err := n.Send(context.Background(), down); err != nil {
			t.Fatalf("%s: %v", cfg.Name, err)
		}
	}
	if !strings.Contains(string((*got)[0].body), `"color":15680580`) {
		t.Errorf("discord should use red for down: %s", (*got)[0].body)
	}
	if !strings.Contains(string((*got)[1].body), "*🔴 API is DOWN*") {
		t.Errorf("slack body: %s", (*got)[1].body)
	}
	hook := (*got)[2]
	if hook.headers.Get("X-Key") != "k" {
		t.Error("webhook headers not sent")
	}
	var payload map[string]any
	if err := json.Unmarshal(hook.body, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["kind"] != "down" || payload["check"] != "API" || payload["title"] == "" {
		t.Errorf("webhook payload = %v", payload)
	}
}

func TestNtfy(t *testing.T) {
	srv, got := recorder(t, http.StatusOK)
	n := &ntfy{name: "n", client: srv.Client(), server: srv.URL, topic: "my alerts", token: "tk"}
	if err := n.Send(context.Background(), down); err != nil {
		t.Fatalf("Send: %v", err)
	}
	c := (*got)[0]
	if c.path != "/my alerts" || c.headers.Get("Priority") != "high" || c.headers.Get("Authorization") != "Bearer tk" {
		t.Errorf("unexpected request %s %v", c.path, c.headers)
	}
}

func TestErrorStatusAndRedaction(t *testing.T) {
	srv, _ := recorder(t, http.StatusBadRequest)
	n := &telegram{name: "tg", client: srv.Client(), apiURL: srv.URL, token: "SECRET", chatID: "1"}
	err := n.Send(context.Background(), down)
	if err == nil || !strings.Contains(err.Error(), "status 400: nope") {
		t.Fatalf("want status error, got %v", err)
	}

	dead := &telegram{name: "tg", client: http.DefaultClient, apiURL: "http://127.0.0.1:1", token: "SECRET", chatID: "1"}
	err = dead.Send(context.Background(), down)
	if err == nil || strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("network errors must not leak the token: %v", err)
	}
}

type flaky struct {
	name  string
	fails int32
	calls atomic.Int32
}

func (f *flaky) Name() string { return f.name }

func (f *flaky) Send(context.Context, Message) error {
	if f.calls.Add(1) <= f.fails {
		return errors.New("boom")
	}
	return nil
}

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestDispatcherRetriesAndTargets(t *testing.T) {
	a := &flaky{name: "a", fails: 2}
	b := &flaky{name: "b"}
	d := NewDispatcher([]Notifier{a, b}, quietLogger())
	d.backoff = time.Millisecond

	d.Dispatch(context.Background(), down, []string{"a"})
	d.Wait()
	if a.calls.Load() != 3 || b.calls.Load() != 0 {
		t.Fatalf("calls a=%d b=%d", a.calls.Load(), b.calls.Load())
	}

	d.Dispatch(context.Background(), down, nil)
	d.Wait()
	if b.calls.Load() != 1 {
		t.Fatal("empty notify list must reach every notifier")
	}
}

func TestDispatcherGivesUp(t *testing.T) {
	a := &flaky{name: "a", fails: 10}
	d := NewDispatcher([]Notifier{a}, quietLogger())
	d.backoff = time.Millisecond
	err := d.SendWithRetry(context.Background(), a, down)
	if err == nil || !strings.Contains(err.Error(), "attempt 3") {
		t.Fatalf("want 3 attempts, got %v", err)
	}
}
