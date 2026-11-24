package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/juanmmm21/infraping/internal/config"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "infraping.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func runCLI(ctx context.Context, args ...string) (int, string, string) {
	var out, errOut bytes.Buffer
	code := run(ctx, args, &out, &errOut, nil)
	return code, out.String(), errOut.String()
}

func TestUsageAndVersion(t *testing.T) {
	if code, _, errOut := runCLI(context.Background()); code != 2 || !strings.Contains(errOut, "Usage") {
		t.Errorf("no args: %d %q", code, errOut)
	}
	if code, out, _ := runCLI(context.Background(), "version"); code != 0 || !strings.Contains(out, "infraping dev") {
		t.Errorf("version: %d %q", code, out)
	}
	path := writeConfig(t, "checks: [{name: a, type: tcp, address: 'h:1'}]")
	if code, _, _ := runCLI(context.Background(), "bogus", "-c", path); code != 2 {
		t.Errorf("unknown command code = %d", code)
	}
}

func TestValidate(t *testing.T) {
	good := writeConfig(t, "checks: [{name: a, type: tcp, address: 'h:1'}]")
	if code, out, _ := runCLI(context.Background(), "validate", "-c", good); code != 0 || !strings.Contains(out, "1 checks") {
		t.Errorf("valid: %d %q", code, out)
	}
	bad := writeConfig(t, "checks: [{name: a, type: smtp}]")
	if code, _, errOut := runCLI(context.Background(), "validate", "-c", bad); code != 1 || !strings.Contains(errOut, "unknown type") {
		t.Errorf("invalid: %d %q", code, errOut)
	}
}

func TestCheckOnce(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/broken" {
			w.WriteHeader(http.StatusBadGateway)
		}
	}))
	defer srv.Close()

	ok := writeConfig(t, fmt.Sprintf("checks: [{name: home, type: http, url: %q}]", srv.URL))
	if code, out, _ := runCLI(context.Background(), "check", "-c", ok); code != 0 || !strings.Contains(out, "UP") {
		t.Errorf("healthy: %d\n%s", code, out)
	}
	bad := writeConfig(t, fmt.Sprintf(`checks:
  - {name: home, type: http, url: %q}
  - {name: broken, type: http, url: %q}`, srv.URL, srv.URL+"/broken"))
	code, out, _ := runCLI(context.Background(), "check", "-c", bad)
	if code != 1 || !strings.Contains(out, "unexpected status 502") || !strings.Contains(out, "1 of 2 checks failing") {
		t.Errorf("failing: %d\n%s", code, out)
	}
}

func TestTestNotify(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
	}))
	defer srv.Close()
	path := writeConfig(t, fmt.Sprintf(`notifiers:
  - {name: hook, type: webhook, url: %q}
checks: [{name: a, type: tcp, address: 'h:1'}]`, srv.URL))

	if code, out, _ := runCLI(context.Background(), "test-notify", "-c", path); code != 0 || !strings.Contains(out, "OK    hook") {
		t.Errorf("test-notify: %d %q", code, out)
	}
	if hits.Load() != 1 {
		t.Errorf("hits = %d", hits.Load())
	}
	if code, _, errOut := runCLI(context.Background(), "test-notify", "-c", path, "-n", "nobody"); code != 1 || !strings.Contains(errOut, "unknown notifier") {
		t.Errorf("unknown notifier: %d %q", code, errOut)
	}
}

func TestRunAlertsAndPersists(t *testing.T) {
	var alerts atomic.Int32
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		alerts.Add(1)
	}))
	defer hook.Close()

	dir := t.TempDir()
	state := filepath.Join(dir, "state.json")
	path := writeConfig(t, fmt.Sprintf(`state_file: %q
status_page: {enabled: true, listen: "127.0.0.1:0"}
notifiers:
  - {name: hook, type: webhook, url: %q}
checks:
  - {name: closed, type: tcp, address: "127.0.0.1:1", interval: 1s, timeout: 500ms, failure_threshold: 1}
`, state, hook.URL))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan int, 1)
	go func() {
		code, _, _ := runCLI(ctx, "run", "-c", path)
		done <- code
	}()
	deadline := time.Now().Add(15 * time.Second)
	for alerts.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	cancel()
	if code := <-done; code != 0 {
		t.Fatalf("run exited with %d", code)
	}
	if alerts.Load() != 1 {
		t.Fatalf("want exactly one down alert, got %d", alerts.Load())
	}
	data, err := os.ReadFile(state)
	if err != nil {
		t.Fatalf("state not saved: %v", err)
	}
	if !strings.Contains(string(data), `"status":"down"`) {
		t.Errorf("state = %s", data)
	}
}

func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	return addr
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestRunReloadsOnSignal(t *testing.T) {
	var mu sync.Mutex
	var bodies []string
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read alert: %v", err)
			return
		}
		mu.Lock()
		bodies = append(bodies, string(body))
		mu.Unlock()
	}))
	defer hook.Close()
	alerted := func(check string) func() bool {
		return func() bool {
			mu.Lock()
			defer mu.Unlock()
			for _, b := range bodies {
				if strings.Contains(b, check) {
					return true
				}
			}
			return false
		}
	}

	addr := freeAddr(t)
	configFor := func(title, check string) string {
		return fmt.Sprintf(`status_page: {enabled: true, listen: %q, title: %q}
notifiers:
  - {name: hook, type: webhook, url: %q}
checks:
  - {name: %s, type: tcp, address: "127.0.0.1:1", interval: 1s, timeout: 500ms, failure_threshold: 1}
`, addr, title, hook.URL, check)
	}
	path := writeConfig(t, configFor("One", "first"))

	reload := make(chan os.Signal, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan int, 1)
	go func() {
		done <- run(ctx, []string{"run", "-c", path}, io.Discard, io.Discard, reload)
	}()
	waitFor(t, "alert for first", alerted("first"))

	// Una configuración rota no tumba el proceso: sigue la anterior.
	if err := os.WriteFile(path, []byte("checks: [{name: x, type: smtp}]"), 0o600); err != nil {
		t.Fatal(err)
	}
	reload <- syscall.SIGHUP
	time.Sleep(300 * time.Millisecond)
	select {
	case code := <-done:
		t.Fatalf("run exited with %d after a bad reload", code)
	default:
	}

	if err := os.WriteFile(path, []byte(configFor("Two", "second")), 0o600); err != nil {
		t.Fatal(err)
	}
	reload <- syscall.SIGHUP
	waitFor(t, "alert for second", alerted("second"))

	var view struct {
		Title  string `json:"title"`
		Checks []struct {
			Name string `json:"name"`
		} `json:"checks"`
	}
	waitFor(t, "status page with the new title", func() bool {
		resp, err := http.Get("http://" + addr + "/api/status")
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		if err := json.NewDecoder(resp.Body).Decode(&view); err != nil {
			t.Errorf("decode status: %v", err)
			return false
		}
		return view.Title == "Two"
	})
	if len(view.Checks) != 1 || view.Checks[0].Name != "second" {
		t.Errorf("status checks after reload = %+v", view.Checks)
	}

	cancel()
	if code := <-done; code != 0 {
		t.Fatalf("run exited with %d", code)
	}
}

func TestRestartOnlyChanges(t *testing.T) {
	old := &config.Config{StateFile: "a.json"}
	old.StatusPage.Enabled = true
	old.StatusPage.Listen = ":8080"
	same := *old
	if got := restartOnlyChanges(old, &same); len(got) != 0 {
		t.Errorf("no changes: %v", got)
	}
	next := *old
	next.StateFile = "b.json"
	next.StatusPage.Listen = ":9090"
	if got := strings.Join(restartOnlyChanges(old, &next), ","); got != "state_file,status_page.listen" {
		t.Errorf("changes = %q", got)
	}
}
