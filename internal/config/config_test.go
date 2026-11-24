package config

import (
	"strings"
	"testing"
	"time"
)

func noEnv(string) (string, bool) { return "", false }

func envOf(vars map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) {
		v, ok := vars[k]
		return v, ok
	}
}

func TestParseAppliesDefaults(t *testing.T) {
	cfg, err := Parse([]byte(`
checks:
  - name: site
    type: http
    url: https://example.com
  - name: cert
    type: tls
    address: example.com:443
notifiers:
  - type: ntfy
    topic: alerts
`), noEnv)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	site := cfg.Checks[0]
	if site.Interval.Std() != time.Minute || site.Timeout.Std() != 10*time.Second {
		t.Errorf("interval/timeout defaults = %s/%s", site.Interval.Std(), site.Timeout.Std())
	}
	if site.Method != "GET" || site.FailureThreshold != 2 || site.RecoveryThreshold != 1 {
		t.Errorf("unexpected http defaults: %+v", site)
	}
	if cfg.Checks[1].MinDaysLeft != 14 {
		t.Errorf("min_days_left default = %d", cfg.Checks[1].MinDaysLeft)
	}
	n := cfg.Notifiers[0]
	if n.Name != "ntfy" || n.Server != "https://ntfy.sh" {
		t.Errorf("ntfy defaults = %+v", n)
	}
	if cfg.StatusPage.Listen != ":8080" || !site.IsPublic() {
		t.Errorf("status page defaults = %+v", cfg.StatusPage)
	}
}

func TestParseExpandsEnvironment(t *testing.T) {
	cfg, err := Parse([]byte(`
notifiers:
  - name: phone
    type: telegram
    token: ${BOT_TOKEN}
    chat_id: "42"
checks:
  - name: db
    type: tcp
    address: db:5432
    notify: [phone]
`), envOf(map[string]string{"BOT_TOKEN": "secret"}))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.Notifiers[0].Token != "secret" {
		t.Errorf("token = %q", cfg.Notifiers[0].Token)
	}
}

func TestParseKeepsBareDollar(t *testing.T) {
	cfg, err := Parse([]byte(`
checks:
  - name: site
    type: http
    url: https://example.com/?q=$price
`), noEnv)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !strings.HasSuffix(cfg.Checks[0].URL, "$price") {
		t.Errorf("url = %q", cfg.Checks[0].URL)
	}
}

func TestParseReportsMissingEnv(t *testing.T) {
	_, err := Parse([]byte(`checks: [{name: a, type: tcp, address: "${HOST}:1"}]`), noEnv)
	if err == nil || !strings.Contains(err.Error(), "HOST") {
		t.Fatalf("want missing HOST error, got %v", err)
	}
}

func TestParseRejectsUnknownFields(t *testing.T) {
	_, err := Parse([]byte(`
checks:
  - name: site
    type: http
    url: https://example.com
    intervall: 5s
`), noEnv)
	if err == nil || !strings.Contains(err.Error(), "intervall") {
		t.Fatalf("want unknown field error, got %v", err)
	}
}

func TestValidateCollectsAllErrors(t *testing.T) {
	_, err := Parse([]byte(`
notifiers:
  - name: chat
    type: telegram
checks:
  - name: a
    type: http
    url: ftp://example.com
    notify: [nobody]
  - name: a
    type: tcp
    address: nohost
  - name: c
    type: smtp
  - name: d
    type: dns
    interval: 5s
    timeout: 10s
`), noEnv)
	if err == nil {
		t.Fatal("want validation error")
	}
	msg := err.Error()
	for _, want := range []string{
		"telegram needs token and chat_id",
		"must be absolute http or https",
		`unknown notifier "nobody"`,
		"duplicate check name",
		"address must be host:port",
		`unknown type "smtp"`,
		"host is required",
		"cannot exceed interval",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("error missing %q:\n%s", want, msg)
		}
	}
}

func TestValidateRequiresChecks(t *testing.T) {
	if _, err := Parse([]byte(`defaults: {interval: 30s}`), noEnv); err == nil {
		t.Fatal("want error for empty checks")
	}
}

func TestBadDuration(t *testing.T) {
	_, err := Parse([]byte(`defaults: {interval: soon}
checks: [{name: a, type: tcp, address: "h:1"}]`), noEnv)
	if err == nil || !strings.Contains(err.Error(), "invalid duration") {
		t.Fatalf("want duration error, got %v", err)
	}
}

func TestPublicFlag(t *testing.T) {
	cfg, err := Parse([]byte(`
checks:
  - name: internal
    type: tcp
    address: 10.0.0.1:22
    public: false
`), noEnv)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.Checks[0].IsPublic() {
		t.Error("public: false should hide the check")
	}
}
