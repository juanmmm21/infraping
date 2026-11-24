// Package notify manda los avisos de caída y recuperación a Telegram, Discord, Slack, ntfy o un webhook.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/juanmmm21/infraping/internal/config"
)

// Kind tells whether a message reports an outage, a recovery or a manual test.
type Kind string

const (
	KindDown      Kind = "down"
	KindRecovered Kind = "recovered"
	KindTest      Kind = "test"
)

// Message is what every notifier receives, already rendered to text.
type Message struct {
	Kind     Kind
	Check    string
	Target   string
	Reason   string
	At       time.Time
	Downtime time.Duration
}

// Title is a short one-line summary.
func (m Message) Title() string {
	switch m.Kind {
	case KindDown:
		return fmt.Sprintf("🔴 %s is DOWN", m.Check)
	case KindRecovered:
		return fmt.Sprintf("🟢 %s is back UP", m.Check)
	default:
		return "🔔 InfraPing test notification"
	}
}

// Body is the human-readable detail below the title.
func (m Message) Body() string {
	var lines []string
	if m.Target != "" {
		lines = append(lines, "Target: "+m.Target)
	}
	if m.Reason != "" {
		lines = append(lines, "Reason: "+m.Reason)
	}
	if m.Kind == KindRecovered && m.Downtime > 0 {
		lines = append(lines, "Downtime: "+formatDuration(m.Downtime))
	}
	lines = append(lines, "At: "+m.At.UTC().Format("2006-01-02 15:04:05 UTC"))
	return strings.Join(lines, "\n")
}

// MarshalJSON manda el tiempo caído en segundos, que es lo que espera cualquier consumidor de un webhook.
func (m Message) MarshalJSON() ([]byte, error) {
	type wire struct {
		Kind            Kind      `json:"kind"`
		Check           string    `json:"check"`
		Target          string    `json:"target,omitempty"`
		Reason          string    `json:"reason,omitempty"`
		At              time.Time `json:"at"`
		DowntimeSeconds int64     `json:"downtime_seconds,omitempty"`
		Title           string    `json:"title"`
		Text            string    `json:"text"`
	}
	return json.Marshal(wire{
		Kind: m.Kind, Check: m.Check, Target: m.Target, Reason: m.Reason, At: m.At,
		DowntimeSeconds: int64(m.Downtime.Seconds()), Title: m.Title(), Text: m.Body(),
	})
}

func formatDuration(d time.Duration) string {
	d = d.Round(time.Second)
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	s := int(d.Seconds()) % 60
	switch {
	case h > 0:
		return fmt.Sprintf("%dh %dm", h, m)
	case m > 0:
		return fmt.Sprintf("%dm %ds", m, s)
	default:
		return fmt.Sprintf("%ds", s)
	}
}

// Notifier delivers a message to one destination.
type Notifier interface {
	Name() string
	Send(ctx context.Context, msg Message) error
}

// New construye el notificador de una entrada ya validada de la configuración.
func New(cfg config.NotifierConfig, client *http.Client) (Notifier, error) {
	switch cfg.Type {
	case config.NotifierTelegram:
		return &telegram{name: cfg.Name, client: client, apiURL: telegramAPI, token: cfg.Token, chatID: cfg.ChatID}, nil
	case config.NotifierDiscord:
		return &discord{name: cfg.Name, client: client, url: cfg.URL}, nil
	case config.NotifierSlack:
		return &slack{name: cfg.Name, client: client, url: cfg.URL}, nil
	case config.NotifierNtfy:
		return &ntfy{name: cfg.Name, client: client, server: strings.TrimRight(cfg.Server, "/"), topic: cfg.Topic, token: cfg.Token}, nil
	case config.NotifierWebhook:
		return &webhook{name: cfg.Name, client: client, url: cfg.URL, headers: cfg.Headers}, nil
	default:
		return nil, fmt.Errorf("notifier %q: unknown type %q", cfg.Name, cfg.Type)
	}
}

// maxErrorBody es lo que se guarda de la respuesta de un servicio que rechaza el aviso: basta para el log.
const maxErrorBody = 512

func post(ctx context.Context, client *http.Client, target, contentType string, body []byte, headers map[string]string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("User-Agent", "infraping")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("post: %w", redactURLError(err))
	}
	defer func() { _ = resp.Body.Close() }() // ya leído o descartado; su error no cambia el resultado
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet, readErr := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
		if readErr != nil {
			return fmt.Errorf("status %d (body unreadable: %w)", resp.StatusCode, readErr)
		}
		return fmt.Errorf("status %d: %s", resp.StatusCode, strings.TrimSpace(string(snippet)))
	}
	return nil
}

// redactURLError quita la URL de los errores de red: en Telegram y en los webhooks lleva el secreto.
func redactURLError(err error) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return fmt.Errorf("%s request failed: %w", urlErr.Op, urlErr.Err)
	}
	return err
}

func postJSON(ctx context.Context, client *http.Client, target string, payload any, headers map[string]string) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode payload: %w", err)
	}
	return post(ctx, client, target, "application/json", body, headers)
}

const telegramAPI = "https://api.telegram.org"

type telegram struct {
	name, apiURL, token, chatID string
	client                      *http.Client
}

func (t *telegram) Name() string { return t.name }

func (t *telegram) Send(ctx context.Context, msg Message) error {
	return postJSON(ctx, t.client, fmt.Sprintf("%s/bot%s/sendMessage", t.apiURL, t.token), map[string]any{
		"chat_id":                  t.chatID,
		"text":                     msg.Title() + "\n\n" + msg.Body(),
		"disable_web_page_preview": true,
	}, nil)
}

type discord struct {
	name, url string
	client    *http.Client
}

func (d *discord) Name() string { return d.name }

func (d *discord) Send(ctx context.Context, msg Message) error {
	color := 0x3b82f6
	switch msg.Kind {
	case KindDown:
		color = 0xef4444
	case KindRecovered:
		color = 0x22c55e
	}
	return postJSON(ctx, d.client, d.url, map[string]any{
		"username": "InfraPing",
		"embeds":   []map[string]any{{"title": msg.Title(), "description": msg.Body(), "color": color}},
	}, nil)
}

type slack struct {
	name, url string
	client    *http.Client
}

func (s *slack) Name() string { return s.name }

func (s *slack) Send(ctx context.Context, msg Message) error {
	return postJSON(ctx, s.client, s.url, map[string]string{"text": "*" + msg.Title() + "*\n" + msg.Body()}, nil)
}

type ntfy struct {
	name, server, topic, token string
	client                     *http.Client
}

func (n *ntfy) Name() string { return n.name }

func (n *ntfy) Send(ctx context.Context, msg Message) error {
	headers := map[string]string{"Title": msg.Title(), "Tags": "bell"}
	switch msg.Kind {
	case KindDown:
		headers["Priority"] = "high"
		headers["Tags"] = "rotating_light"
	case KindRecovered:
		headers["Tags"] = "white_check_mark"
	}
	if n.token != "" {
		headers["Authorization"] = "Bearer " + n.token
	}
	return post(ctx, n.client, n.server+"/"+url.PathEscape(n.topic), "text/plain; charset=utf-8", []byte(msg.Body()), headers)
}

type webhook struct {
	name, url string
	headers   map[string]string
	client    *http.Client
}

func (w *webhook) Name() string { return w.name }

func (w *webhook) Send(ctx context.Context, msg Message) error {
	return postJSON(ctx, w.client, w.url, msg, w.headers)
}

// Dispatcher envía cada aviso a sus destinos en paralelo, con reintentos, sin bloquear las sondas.
type Dispatcher struct {
	notifiers map[string]Notifier
	order     []string
	attempts  int
	backoff   time.Duration
	timeout   time.Duration
	logger    *slog.Logger
	wg        sync.WaitGroup
}

// NewDispatcher wraps the notifiers; attempts and backoff control retries.
func NewDispatcher(notifiers []Notifier, logger *slog.Logger) *Dispatcher {
	d := &Dispatcher{
		notifiers: make(map[string]Notifier, len(notifiers)),
		attempts:  3,
		backoff:   2 * time.Second,
		timeout:   15 * time.Second,
		logger:    logger,
	}
	for _, n := range notifiers {
		d.notifiers[n.Name()] = n
		d.order = append(d.order, n.Name())
	}
	return d
}

// Targets resuelve a quién avisar: los nombrados en la comprobación o, si no nombra ninguno, todos.
func (d *Dispatcher) Targets(names []string) []Notifier {
	if len(names) == 0 {
		names = d.order
	}
	out := make([]Notifier, 0, len(names))
	for _, name := range names {
		if n, ok := d.notifiers[name]; ok {
			out = append(out, n)
		}
	}
	return out
}

// Dispatch lanza el envío en segundo plano; Wait espera a que terminen todos los pendientes.
func (d *Dispatcher) Dispatch(ctx context.Context, msg Message, names []string) {
	for _, n := range d.Targets(names) {
		d.wg.Add(1)
		go func() {
			defer d.wg.Done()
			if err := d.SendWithRetry(ctx, n, msg); err != nil {
				d.logger.Error("notification failed", "notifier", n.Name(), "check", msg.Check, "kind", msg.Kind, "err", err)
				return
			}
			d.logger.Info("notification sent", "notifier", n.Name(), "check", msg.Check, "kind", msg.Kind)
		}()
	}
}

// SendWithRetry reintenta con espera creciente; un servicio de avisos caído un momento no debe tragarse una caída.
func (d *Dispatcher) SendWithRetry(ctx context.Context, n Notifier, msg Message) error {
	var errs []error
	for attempt := range d.attempts {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return errors.Join(append(errs, ctx.Err())...)
			case <-time.After(d.backoff * time.Duration(1<<(attempt-1))):
			}
		}
		sendCtx, cancel := context.WithTimeout(ctx, d.timeout)
		err := n.Send(sendCtx, msg)
		cancel()
		if err == nil {
			return nil
		}
		errs = append(errs, fmt.Errorf("attempt %d: %w", attempt+1, err))
	}
	return errors.Join(errs...)
}

// Wait blocks until every dispatched message has been sent or given up.
func (d *Dispatcher) Wait() { d.wg.Wait() }
