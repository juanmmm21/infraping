// Package check implementa las sondas: HTTP, TCP, certificado TLS y DNS.
package check

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/juanmmm21/infraping/internal/config"
)

// Result is the outcome of running a check once.
type Result struct {
	OK        bool          `json:"ok"`
	Latency   time.Duration `json:"latency"`
	Message   string        `json:"message"`
	CheckedAt time.Time     `json:"checked_at"`
}

// Checker runs one probe. Implementations must respect ctx's deadline.
type Checker interface {
	Check(ctx context.Context) Result
}

// maxBodyBytes limita lo que se lee de una respuesta para buscar "contains": basta para una página,
// y evita que un endpoint enorme o infinito se coma la memoria.
const maxBodyBytes = 1 << 20

// New construye la sonda de una comprobación ya validada.
func New(cfg config.CheckConfig) (Checker, error) {
	var inner Checker
	switch cfg.Type {
	case config.CheckHTTP:
		inner = newHTTP(cfg)
	case config.CheckTCP:
		inner = &tcpCheck{address: cfg.Address}
	case config.CheckTLS:
		inner = &tlsCheck{address: cfg.Address, minDaysLeft: cfg.MinDaysLeft, now: time.Now}
	case config.CheckDNS:
		inner = newDNS(cfg)
	default:
		return nil, fmt.Errorf("check %q: unknown type %q", cfg.Name, cfg.Type)
	}
	return &bounded{inner: inner, timeout: cfg.Timeout.Std(), maxLatency: cfg.MaxLatency.Std()}, nil
}

// bounded aplica el timeout, mide la latencia y marca como fallo lo que va más lento de lo permitido.
type bounded struct {
	inner      Checker
	timeout    time.Duration
	maxLatency time.Duration
}

func (b *bounded) Check(ctx context.Context) Result {
	ctx, cancel := context.WithTimeout(ctx, b.timeout)
	defer cancel()
	start := time.Now()
	res := b.inner.Check(ctx)
	res.Latency = time.Since(start)
	res.CheckedAt = start
	if res.OK && b.maxLatency > 0 && res.Latency > b.maxLatency {
		res.OK = false
		res.Message = fmt.Sprintf("slow response: %s (limit %s)", res.Latency.Round(time.Millisecond), b.maxLatency)
	}
	return res
}

func fail(format string, args ...any) Result {
	return Result{OK: false, Message: fmt.Sprintf(format, args...)}
}

// describeErr traduce los errores de red más comunes a una frase que se entiende en una notificación.
func describeErr(err error) string {
	var dnsErr *net.DNSError
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "timed out"
	case errors.As(err, &dnsErr):
		if dnsErr.IsNotFound {
			return fmt.Sprintf("DNS: %s not found", dnsErr.Name)
		}
		return fmt.Sprintf("DNS error: %s", dnsErr.Err)
	default:
		return err.Error()
	}
}

type httpCheck struct {
	client       *http.Client
	method       string
	url          string
	headers      map[string]string
	expectStatus []int
	contains     string
}

func newHTTP(cfg config.CheckConfig) *httpCheck {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	// Sin conexiones reutilizadas: cada sonda tiene que medir la conexión entera, como un visitante nuevo.
	transport.DisableKeepAlives = true
	if cfg.SkipTLSVerify {
		// Lo pide el usuario a propósito, para servicios internos con certificado autofirmado.
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}
	return &httpCheck{
		client:       &http.Client{Transport: transport},
		method:       cfg.Method,
		url:          cfg.URL,
		headers:      cfg.Headers,
		expectStatus: cfg.ExpectStatus,
		contains:     cfg.Contains,
	}
}

func (h *httpCheck) Check(ctx context.Context) Result {
	req, err := http.NewRequestWithContext(ctx, h.method, h.url, nil)
	if err != nil {
		return fail("build request: %v", err)
	}
	req.Header.Set("User-Agent", "infraping")
	for k, v := range h.headers {
		req.Header.Set(k, v)
	}
	resp, err := h.client.Do(req)
	if err != nil {
		return fail("%s", describeErr(err))
	}
	defer func() { _ = resp.Body.Close() }() // el cierre de un cuerpo ya leído no aporta nada que avisar
	if !h.statusOK(resp.StatusCode) {
		return fail("unexpected status %d", resp.StatusCode)
	}
	if h.contains != "" {
		body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
		if err != nil {
			return fail("read body: %s", describeErr(err))
		}
		if !strings.Contains(string(body), h.contains) {
			return fail("body does not contain %q", h.contains)
		}
	}
	return Result{OK: true, Message: fmt.Sprintf("status %d", resp.StatusCode)}
}

func (h *httpCheck) statusOK(code int) bool {
	if len(h.expectStatus) == 0 {
		return code >= 200 && code < 400
	}
	return slices.Contains(h.expectStatus, code)
}

type tcpCheck struct {
	address string
}

func (t *tcpCheck) Check(ctx context.Context) Result {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", t.address)
	if err != nil {
		return fail("connect %s: %s", t.address, describeErr(err))
	}
	if err := conn.Close(); err != nil {
		return fail("close %s: %v", t.address, err)
	}
	return Result{OK: true, Message: "port open"}
}

type tlsCheck struct {
	address     string
	minDaysLeft int
	now         func() time.Time
	// base permite a los tests confiar en un certificado propio sin tocar las raíces del sistema.
	base *tls.Config
}

func (t *tlsCheck) Check(ctx context.Context) Result {
	host, _, err := net.SplitHostPort(t.address)
	if err != nil {
		return fail("invalid address %q: %v", t.address, err)
	}
	cfg := &tls.Config{}
	if t.base != nil {
		cfg = t.base.Clone()
	}
	cfg.ServerName = host
	dialer := &tls.Dialer{Config: cfg}
	conn, err := dialer.DialContext(ctx, "tcp", t.address)
	if err != nil {
		return fail("TLS handshake with %s: %s", t.address, describeErr(err))
	}
	tlsConn, ok := conn.(*tls.Conn)
	if !ok {
		_ = conn.Close() // conexión inútil: el error relevante es el de tipo
		return fail("unexpected connection type %T", conn)
	}
	certs := tlsConn.ConnectionState().PeerCertificates
	if err := conn.Close(); err != nil {
		return fail("close %s: %v", t.address, err)
	}
	if len(certs) == 0 {
		return fail("no certificate presented")
	}
	leaf := certs[0]
	daysLeft := int(leaf.NotAfter.Sub(t.now()).Hours() / 24)
	if daysLeft < t.minDaysLeft {
		return fail("certificate expires in %d days (%s)", daysLeft, leaf.NotAfter.Format("2006-01-02"))
	}
	return Result{OK: true, Message: fmt.Sprintf("certificate valid for %d days", daysLeft)}
}

type dnsCheck struct {
	host      string
	expectIPs []string
	resolver  *net.Resolver
}

func newDNS(cfg config.CheckConfig) *dnsCheck {
	resolver := net.DefaultResolver
	if cfg.Resolver != "" {
		server := cfg.Resolver
		resolver = &net.Resolver{
			PreferGo: true,
			Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, network, server)
			},
		}
	}
	return &dnsCheck{host: cfg.Host, expectIPs: cfg.ExpectIPs, resolver: resolver}
}

func (d *dnsCheck) Check(ctx context.Context) Result {
	addrs, err := d.resolver.LookupHost(ctx, d.host)
	if err != nil {
		return fail("%s", describeErr(err))
	}
	if len(addrs) == 0 {
		return fail("no addresses for %s", d.host)
	}
	if len(d.expectIPs) > 0 && !slices.ContainsFunc(addrs, func(a string) bool { return slices.Contains(d.expectIPs, a) }) {
		return fail("%s resolves to %s, none of the expected %s", d.host, strings.Join(addrs, ", "), strings.Join(d.expectIPs, ", "))
	}
	return Result{OK: true, Message: "resolves to " + strings.Join(addrs, ", ")}
}
