// Package config carga y valida el fichero YAML que describe qué vigilar y a quién avisar.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

// Duration acepta valores como "30s" o "5m" en el YAML.
type Duration time.Duration

// UnmarshalYAML convierte una cadena de duración de Go en Duration.
func (d *Duration) UnmarshalYAML(node *yaml.Node) error {
	var raw string
	if err := node.Decode(&raw); err != nil {
		return fmt.Errorf("line %d: duration must be a string like \"30s\": %w", node.Line, err)
	}
	parsed, err := time.ParseDuration(raw)
	if err != nil {
		return fmt.Errorf("line %d: invalid duration %q: %w", node.Line, raw, err)
	}
	*d = Duration(parsed)
	return nil
}

// Std devuelve la duración como time.Duration.
func (d Duration) Std() time.Duration { return time.Duration(d) }

// Check types.
const (
	CheckHTTP = "http"
	CheckTCP  = "tcp"
	CheckTLS  = "tls"
	CheckDNS  = "dns"
)

// Notifier types.
const (
	NotifierTelegram = "telegram"
	NotifierDiscord  = "discord"
	NotifierSlack    = "slack"
	NotifierNtfy     = "ntfy"
	NotifierWebhook  = "webhook"
)

// Defaults are applied to every check that does not set its own value.
type Defaults struct {
	Interval          Duration `yaml:"interval"`
	Timeout           Duration `yaml:"timeout"`
	FailureThreshold  int      `yaml:"failure_threshold"`
	RecoveryThreshold int      `yaml:"recovery_threshold"`
}

// StatusPage configures the built-in public status page.
type StatusPage struct {
	Enabled bool   `yaml:"enabled"`
	Listen  string `yaml:"listen"`
	Title   string `yaml:"title"`
}

// NotifierConfig describes one alert destination.
type NotifierConfig struct {
	Name    string            `yaml:"name"`
	Type    string            `yaml:"type"`
	Token   string            `yaml:"token"`
	ChatID  string            `yaml:"chat_id"`
	URL     string            `yaml:"url"`
	Server  string            `yaml:"server"`
	Topic   string            `yaml:"topic"`
	Headers map[string]string `yaml:"headers"`
}

// CheckConfig describes one thing to watch.
type CheckConfig struct {
	Name              string            `yaml:"name"`
	Type              string            `yaml:"type"`
	URL               string            `yaml:"url"`
	Method            string            `yaml:"method"`
	Headers           map[string]string `yaml:"headers"`
	ExpectStatus      []int             `yaml:"expect_status"`
	Contains          string            `yaml:"contains"`
	SkipTLSVerify     bool              `yaml:"skip_tls_verify"`
	Address           string            `yaml:"address"`
	Host              string            `yaml:"host"`
	Resolver          string            `yaml:"resolver"`
	ExpectIPs         []string          `yaml:"expect_ips"`
	MinDaysLeft       int               `yaml:"min_days_left"`
	MaxLatency        Duration          `yaml:"max_latency"`
	Interval          Duration          `yaml:"interval"`
	Timeout           Duration          `yaml:"timeout"`
	FailureThreshold  int               `yaml:"failure_threshold"`
	RecoveryThreshold int               `yaml:"recovery_threshold"`
	Notify            []string          `yaml:"notify"`
	Public            *bool             `yaml:"public"`
}

// IsPublic dice si la comprobación aparece en la página de estado (por defecto, sí).
func (c CheckConfig) IsPublic() bool { return c.Public == nil || *c.Public }

// Config is the whole configuration file.
type Config struct {
	Defaults   Defaults         `yaml:"defaults"`
	StateFile  string           `yaml:"state_file"`
	StatusPage StatusPage       `yaml:"status_page"`
	Notifiers  []NotifierConfig `yaml:"notifiers"`
	Checks     []CheckConfig    `yaml:"checks"`
}

const (
	defaultInterval        = 60 * time.Second
	defaultTimeout         = 10 * time.Second
	defaultFailureThresh   = 2
	defaultRecoveryThresh  = 1
	defaultMinDaysLeft     = 14
	defaultStatusListen    = ":8080"
	defaultStatusPageTitle = "Service status"
	defaultNtfyServer      = "https://ntfy.sh"
	minInterval            = time.Second
)

var envRef = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// Load lee, expande las variables de entorno, aplica valores por defecto y valida el fichero.
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	return Parse(raw, os.LookupEnv)
}

// Parse hace lo mismo que Load sobre bytes ya leídos; lookup resuelve las ${VARIABLES}.
func Parse(raw []byte, lookup func(string) (string, bool)) (*Config, error) {
	expanded, err := expandEnv(raw, lookup)
	if err != nil {
		return nil, err
	}
	var cfg Config
	dec := yaml.NewDecoder(bytes.NewReader(expanded))
	// Un campo mal escrito en el YAML debe fallar en vez de ignorarse en silencio.
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	cfg.applyDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// expandEnv sustituye solo la forma ${VAR}: un "$" suelto en una URL o una contraseña se queda tal cual.
func expandEnv(raw []byte, lookup func(string) (string, bool)) ([]byte, error) {
	var missing []string
	out := envRef.ReplaceAllFunc(raw, func(m []byte) []byte {
		name := string(envRef.FindSubmatch(m)[1])
		value, ok := lookup(name)
		if !ok {
			missing = append(missing, name)
			return m
		}
		return []byte(value)
	})
	if len(missing) > 0 {
		return nil, fmt.Errorf("config references unset environment variables: %s", strings.Join(missing, ", "))
	}
	return out, nil
}

func (c *Config) applyDefaults() {
	if c.Defaults.Interval == 0 {
		c.Defaults.Interval = Duration(defaultInterval)
	}
	if c.Defaults.Timeout == 0 {
		c.Defaults.Timeout = Duration(defaultTimeout)
	}
	if c.Defaults.FailureThreshold == 0 {
		c.Defaults.FailureThreshold = defaultFailureThresh
	}
	if c.Defaults.RecoveryThreshold == 0 {
		c.Defaults.RecoveryThreshold = defaultRecoveryThresh
	}
	if c.StatusPage.Listen == "" {
		c.StatusPage.Listen = defaultStatusListen
	}
	if c.StatusPage.Title == "" {
		c.StatusPage.Title = defaultStatusPageTitle
	}
	for i := range c.Notifiers {
		n := &c.Notifiers[i]
		if n.Name == "" {
			n.Name = n.Type
		}
		if n.Type == NotifierNtfy && n.Server == "" {
			n.Server = defaultNtfyServer
		}
	}
	for i := range c.Checks {
		ch := &c.Checks[i]
		if ch.Interval == 0 {
			ch.Interval = c.Defaults.Interval
		}
		if ch.Timeout == 0 {
			ch.Timeout = c.Defaults.Timeout
		}
		if ch.FailureThreshold == 0 {
			ch.FailureThreshold = c.Defaults.FailureThreshold
		}
		if ch.RecoveryThreshold == 0 {
			ch.RecoveryThreshold = c.Defaults.RecoveryThreshold
		}
		if ch.Type == CheckHTTP && ch.Method == "" {
			ch.Method = "GET"
		}
		if ch.Type == CheckTLS && ch.MinDaysLeft == 0 {
			ch.MinDaysLeft = defaultMinDaysLeft
		}
	}
}

// Validate devuelve todos los problemas del fichero juntos, para arreglarlos de una vez.
func (c *Config) Validate() error {
	var errs []error
	if len(c.Checks) == 0 {
		errs = append(errs, errors.New("no checks defined"))
	}
	notifierNames := make(map[string]bool, len(c.Notifiers))
	for i, n := range c.Notifiers {
		label := fmt.Sprintf("notifiers[%d] (%s)", i, n.Name)
		if notifierNames[n.Name] {
			errs = append(errs, fmt.Errorf("%s: duplicate notifier name", label))
		}
		notifierNames[n.Name] = true
		errs = append(errs, validateNotifier(label, n)...)
	}
	checkNames := make(map[string]bool, len(c.Checks))
	for i, ch := range c.Checks {
		label := fmt.Sprintf("checks[%d] (%s)", i, ch.Name)
		if ch.Name == "" {
			errs = append(errs, fmt.Errorf("checks[%d]: name is required", i))
		} else if checkNames[ch.Name] {
			errs = append(errs, fmt.Errorf("%s: duplicate check name", label))
		}
		checkNames[ch.Name] = true
		errs = append(errs, validateCheck(label, ch)...)
		for _, target := range ch.Notify {
			if !notifierNames[target] {
				errs = append(errs, fmt.Errorf("%s: unknown notifier %q", label, target))
			}
		}
	}
	return errors.Join(errs...)
}

func validateCheck(label string, ch CheckConfig) []error {
	var errs []error
	if ch.Interval.Std() < minInterval {
		errs = append(errs, fmt.Errorf("%s: interval must be at least %s", label, minInterval))
	}
	if ch.Timeout.Std() <= 0 {
		errs = append(errs, fmt.Errorf("%s: timeout must be positive", label))
	}
	if ch.Timeout.Std() > ch.Interval.Std() {
		errs = append(errs, fmt.Errorf("%s: timeout (%s) cannot exceed interval (%s)", label, ch.Timeout.Std(), ch.Interval.Std()))
	}
	if ch.FailureThreshold < 1 || ch.RecoveryThreshold < 1 {
		errs = append(errs, fmt.Errorf("%s: thresholds must be at least 1", label))
	}
	switch ch.Type {
	case CheckHTTP:
		if err := validateHTTPURL(ch.URL); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", label, err))
		}
		for _, code := range ch.ExpectStatus {
			if code < 100 || code > 599 {
				errs = append(errs, fmt.Errorf("%s: invalid expect_status %d", label, code))
			}
		}
	case CheckTCP, CheckTLS:
		if !strings.Contains(ch.Address, ":") {
			errs = append(errs, fmt.Errorf("%s: address must be host:port", label))
		}
		if ch.MinDaysLeft < 0 {
			errs = append(errs, fmt.Errorf("%s: min_days_left cannot be negative", label))
		}
	case CheckDNS:
		if ch.Host == "" {
			errs = append(errs, fmt.Errorf("%s: host is required", label))
		}
		if ch.Resolver != "" && !strings.Contains(ch.Resolver, ":") {
			errs = append(errs, fmt.Errorf("%s: resolver must be host:port", label))
		}
	default:
		errs = append(errs, fmt.Errorf("%s: unknown type %q (want http, tcp, tls or dns)", label, ch.Type))
	}
	return errs
}

func validateNotifier(label string, n NotifierConfig) []error {
	var errs []error
	switch n.Type {
	case NotifierTelegram:
		if n.Token == "" || n.ChatID == "" {
			errs = append(errs, fmt.Errorf("%s: telegram needs token and chat_id", label))
		}
	case NotifierDiscord, NotifierSlack, NotifierWebhook:
		if err := validateHTTPURL(n.URL); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", label, err))
		}
	case NotifierNtfy:
		if n.Topic == "" {
			errs = append(errs, fmt.Errorf("%s: ntfy needs a topic", label))
		}
		if err := validateHTTPURL(n.Server); err != nil {
			errs = append(errs, fmt.Errorf("%s: server: %w", label, err))
		}
	default:
		errs = append(errs, fmt.Errorf("%s: unknown type %q (want telegram, discord, slack, ntfy or webhook)", label, n.Type))
	}
	return errs
}

func validateHTTPURL(raw string) error {
	if raw == "" {
		return errors.New("url is required")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid url %q: %w", raw, err)
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("url %q must be absolute http or https", raw)
	}
	return nil
}
