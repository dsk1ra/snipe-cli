// Package providers is the portal-scan plugin layer.
//
// A provider turns one portals.yml entry into a list of postings. It never
// calls the network itself — it takes a Fetcher — which is what makes every
// parse and normalisation path reachable from a test with canned payloads and
// no server at all. That design came from the Node implementation and maps onto
// a Go interface unchanged.
package providers

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// Job is the unit of currency throughout the scanner.
type Job struct {
	Title    string
	URL      string // absolute; the dedup key
	Company  string // may be empty when the source cannot expose it at list level
	Location string // may be empty
	// Source is the portals.yml entry the job came from. The providers do not
	// set it; the scan does, once it knows which entry produced the job.
	Source string
	// PostedAt is epoch milliseconds, zero when the source exposes no usable
	// date. The age filter is fail-closed, so zero means dropped.
	PostedAt int64
}

// Entry is one tracked_companies entry.
//
// Provider-specific keys (api_key, actor_id, field_map, parser, …) stay in
// Extra rather than being enumerated here, so adding one to a provider never
// requires touching this shared type.
type Entry struct {
	Name        string         `yaml:"name"`
	Enabled     *bool          `yaml:"enabled"` // pointer: absent means true
	CareersURL  string         `yaml:"careers_url"`
	API         string         `yaml:"api"`
	Provider    string         `yaml:"provider"` // explicit id, bypassing detection
	Transport   string         `yaml:"transport"`
	MaxAgeHours *int           `yaml:"max_age_hours"`
	Extra       map[string]any `yaml:",inline"`
}

func (e Entry) On() bool { return e.Enabled == nil || *e.Enabled }

// Str reads a provider-specific string off Extra.
func (e Entry) Str(key string) string {
	if v, ok := e.Extra[key].(string); ok {
		return v
	}
	return ""
}

// Fetcher is the transport. Providers take it rather than reaching for the
// network, so a stub returning canned payloads reaches every parse path.
type Fetcher interface {
	JSON(ctx context.Context, url string, opts Options, out any) error
	Text(ctx context.Context, url string, opts Options) (string, error)
}

// Options are per-request transport knobs.
type Options struct {
	Headers map[string]string
	// NoRedirect refuses to follow redirects. Combined with a hostname
	// allowlist it is what closes SSRF via a server-side redirect: the
	// allowlist checks the URL we send, the refusal stops it becoming another.
	NoRedirect bool
}

// Provider turns one entry into postings.
type Provider interface {
	ID() string
	// Detect claims an entry by inspecting its careers_url. Returning false
	// means "not mine"; routing only cares about the boolean.
	Detect(e Entry) bool
	Fetch(ctx context.Context, e Entry, f Fetcher) ([]Job, error)
}

var (
	mu       sync.RWMutex
	registry = map[string]Provider{}
)

// Register adds a provider. Called from each provider's init.
func Register(p Provider) {
	mu.Lock()
	defer mu.Unlock()
	if _, dup := registry[p.ID()]; dup {
		panic("duplicate provider id: " + p.ID())
	}
	registry[p.ID()] = p
}

func Get(id string) (Provider, bool) {
	mu.RLock()
	defer mu.RUnlock()
	p, ok := registry[id]
	return p, ok
}

// All returns every provider, id-ordered so detection priority is
// deterministic across machines.
func All() []Provider {
	mu.RLock()
	defer mu.RUnlock()
	ids := make([]string, 0, len(registry))
	for id := range registry {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]Provider, len(ids))
	for i, id := range ids {
		out[i] = registry[id]
	}
	return out
}

// Resolve picks the provider for an entry: an explicit `provider:` wins,
// otherwise the first to claim it in id order.
func Resolve(e Entry) (Provider, error) {
	if e.Provider != "" {
		p, ok := Get(e.Provider)
		if !ok {
			return nil, fmt.Errorf("%s: unknown provider %q", e.Name, e.Provider)
		}
		return p, nil
	}
	for _, p := range All() {
		if p.Detect(e) {
			return p, nil
		}
	}
	return nil, fmt.Errorf("%s: no provider matched %q", e.Name, e.CareersURL)
}

// hostAllowed guards against a misconfigured portals.yml or a hijacked feed
// pointing the scanner somewhere it should not go. Providers that hit a known
// API check the hostname before the request and refuse redirects during it.
func hostAllowed(host string, allowed ...string) bool {
	host = strings.ToLower(host)
	for _, a := range allowed {
		if host == a {
			return true
		}
	}
	return false
}
