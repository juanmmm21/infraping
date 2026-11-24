package check

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/juanmmm21/infraping/internal/config"
)

func httpCfg(url string) config.CheckConfig {
	return config.CheckConfig{Name: "t", Type: config.CheckHTTP, URL: url, Method: "GET", Timeout: config.Duration(2 * time.Second)}
}

func mustNew(t *testing.T, cfg config.CheckConfig) Checker {
	t.Helper()
	c, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

func TestHTTPOK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Token") != "abc" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte("hello world"))
	}))
	defer srv.Close()

	cfg := httpCfg(srv.URL)
	cfg.Headers = map[string]string{"X-Token": "abc"}
	cfg.Contains = "world"
	res := mustNew(t, cfg).Check(context.Background())
	if !res.OK {
		t.Fatalf("want OK, got %+v", res)
	}
	if res.CheckedAt.IsZero() || res.Latency <= 0 {
		t.Errorf("timing not recorded: %+v", res)
	}
}

func TestHTTPFailures(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/500":
			w.WriteHeader(http.StatusInternalServerError)
		case "/slow":
			time.Sleep(300 * time.Millisecond)
		default:
			_, _ = w.Write([]byte("maintenance"))
		}
	}))
	defer srv.Close()

	cases := []struct {
		name string
		mod  func(*config.CheckConfig)
		want string
	}{
		{"status", func(c *config.CheckConfig) { c.URL += "/500" }, "unexpected status 500"},
		{"expect list", func(c *config.CheckConfig) { c.ExpectStatus = []int{204} }, "unexpected status 200"},
		{"contains", func(c *config.CheckConfig) { c.Contains = "welcome" }, `does not contain "welcome"`},
		{"timeout", func(c *config.CheckConfig) {
			c.URL += "/slow"
			c.Timeout = config.Duration(50 * time.Millisecond)
		}, "timed out"},
		{"max latency", func(c *config.CheckConfig) {
			c.URL += "/slow"
			c.MaxLatency = config.Duration(50 * time.Millisecond)
		}, "slow response"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := httpCfg(srv.URL)
			tc.mod(&cfg)
			res := mustNew(t, cfg).Check(context.Background())
			if res.OK || !strings.Contains(res.Message, tc.want) {
				t.Fatalf("want failure %q, got %+v", tc.want, res)
			}
		})
	}
}

func TestTCP(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	cfg := config.CheckConfig{Name: "t", Type: config.CheckTCP, Address: addr, Timeout: config.Duration(time.Second)}
	if res := mustNew(t, cfg).Check(context.Background()); !res.OK {
		t.Fatalf("open port: %+v", res)
	}
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	if res := mustNew(t, cfg).Check(context.Background()); res.OK {
		t.Fatalf("closed port should fail: %+v", res)
	}
}

func tlsCheckFor(t *testing.T, srv *httptest.Server, minDays int, now time.Time) *tlsCheck {
	t.Helper()
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	return &tlsCheck{
		address:     srv.Listener.Addr().String(),
		minDaysLeft: minDays,
		now:         func() time.Time { return now },
		base:        &tls.Config{RootCAs: pool},
	}
}

func TestTLSExpiry(t *testing.T) {
	srv := httptest.NewTLSServer(http.NotFoundHandler())
	defer srv.Close()
	notAfter := srv.Certificate().NotAfter

	ok := tlsCheckFor(t, srv, 14, notAfter.Add(-30*24*time.Hour)).Check(context.Background())
	if !ok.OK || !strings.Contains(ok.Message, "valid for 30 days") {
		t.Fatalf("want valid, got %+v", ok)
	}
	soon := tlsCheckFor(t, srv, 14, notAfter.Add(-5*24*time.Hour)).Check(context.Background())
	if soon.OK || !strings.Contains(soon.Message, "expires in 5 days") {
		t.Fatalf("want expiring, got %+v", soon)
	}
}

func TestTLSUntrusted(t *testing.T) {
	srv := httptest.NewTLSServer(http.NotFoundHandler())
	defer srv.Close()
	c := &tlsCheck{address: srv.Listener.Addr().String(), minDaysLeft: 1, now: time.Now}
	if res := c.Check(context.Background()); res.OK || !strings.Contains(res.Message, "TLS handshake") {
		t.Fatalf("self-signed cert must fail verification: %+v", res)
	}
}

func TestDNSLocalhost(t *testing.T) {
	c := &dnsCheck{host: "localhost", resolver: net.DefaultResolver}
	if res := c.Check(context.Background()); !res.OK {
		t.Fatalf("localhost should resolve: %+v", res)
	}
	c.expectIPs = []string{"203.0.113.7"}
	if res := c.Check(context.Background()); res.OK || !strings.Contains(res.Message, "none of the expected") {
		t.Fatalf("want mismatch, got %+v", res)
	}
}

func TestNewRejectsUnknownType(t *testing.T) {
	if _, err := New(config.CheckConfig{Name: "x", Type: "smtp"}); err == nil {
		t.Fatal("want error")
	}
}
