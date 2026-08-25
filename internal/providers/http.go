package providers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	defaultTimeout = 10 * time.Second
	userAgent      = "Mozilla/5.0 (compatible; snipe/1.3)"
)

// HTTP is the real transport.
type HTTP struct {
	Client  *http.Client
	Timeout time.Duration
}

func NewHTTP() *HTTP {
	return &HTTP{
		// One client, so connections are pooled across the whole scan.
		Client:  &http.Client{},
		Timeout: defaultTimeout,
	}
}

// StatusError carries the HTTP status so a caller can tell "gone" from "try
// again later".
type StatusError struct {
	Status  int
	URL     string
	Snippet string
}

func (e *StatusError) Error() string {
	if e.Snippet != "" {
		return fmt.Sprintf("HTTP %d from %s: %s", e.Status, e.URL, e.Snippet)
	}
	return fmt.Sprintf("HTTP %d from %s", e.Status, e.URL)
}

// Gone reports whether the status means the resource will not come back.
func (e *StatusError) Gone() bool {
	return e.Status == http.StatusNotFound || e.Status == http.StatusForbidden || e.Status == http.StatusGone
}

var errRedirect = errors.New("redirect refused")

func (h *HTTP) do(ctx context.Context, target string, opts Options, accept string) ([]byte, error) {
	timeout := h.Timeout
	if timeout == 0 {
		timeout = defaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", accept)
	for k, v := range opts.Headers {
		req.Header.Set(k, v)
	}

	client := h.Client
	if opts.NoRedirect {
		// A per-request copy: mutating the shared client's CheckRedirect would
		// race every other in-flight scan request.
		c := *h.Client
		c.CheckRedirect = func(*http.Request, []*http.Request) error { return errRedirect }
		client = &c
	}

	res, err := client.Do(req)
	if err != nil {
		if errors.Is(err, errRedirect) {
			return nil, fmt.Errorf("%s: redirect refused (SSRF guard)", target)
		}
		return nil, err
	}
	defer res.Body.Close()

	body, err := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if res.StatusCode != http.StatusOK {
		return nil, &StatusError{
			Status: res.StatusCode, URL: target,
			Snippet: clip(strings.Join(strings.Fields(string(body)), " "), 300),
		}
	}
	return body, nil
}

func (h *HTTP) JSON(ctx context.Context, target string, opts Options, out any) error {
	b, err := h.do(ctx, target, opts, "application/json")
	if err != nil {
		return err
	}
	return json.Unmarshal(b, out)
}

func (h *HTTP) Text(ctx context.Context, target string, opts Options) (string, error) {
	b, err := h.do(ctx, target, opts, "text/html,application/xhtml+xml,application/json")
	return string(b), err
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// httpsHost validates a URL and returns its hostname. Non-HTTPS is refused
// outright: every board this scanner talks to serves HTTPS, and a plain-HTTP
// entry in portals.yml is a misconfiguration worth failing on rather than
// silently downgrading.
func httpsHost(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("invalid URL: %s", raw)
	}
	if u.Scheme != "https" {
		return "", fmt.Errorf("URL must use HTTPS: %s", raw)
	}
	return strings.ToLower(u.Hostname()), nil
}

// assertHost is the pre-request half of the SSRF guard. The other half is
// Options.NoRedirect: together they guarantee the hostname we checked is the
// hostname that answers.
func assertHost(who, raw string, allowed ...string) error {
	host, err := httpsHost(raw)
	if err != nil {
		return fmt.Errorf("%s: %w", who, err)
	}
	if !hostAllowed(host, allowed...) {
		return fmt.Errorf("%s: untrusted hostname %q — must be one of: %s",
			who, host, strings.Join(allowed, ", "))
	}
	return nil
}

func epochMS(s string) int64 {
	if s == "" {
		return 0
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05Z0700", "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UnixMilli()
		}
	}
	return 0
}
