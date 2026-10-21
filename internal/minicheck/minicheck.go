// Package minicheck asks Bespoke-MiniCheck whether a document supports a claim.
//
// MiniCheck is a fact-checking model that answers a bare "Yes" or "No"
// (docs/PRD-TAILORING-REWORK.md §3.5). It is CC BY-NC 4.0, so the repository
// never ships it; the user pulls it into Ollama as `bespoke-minicheck`.
package minicheck

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/dsk1ra/snipe-cli/internal/ollama"
)

const Model = "bespoke-minicheck"

// Client wraps the shared Ollama client; the zero value is not usable.
type Client struct {
	OL *ollama.Client
}

// Check reports whether document supports claim.
//
// Anything but a clean yes or no is an error. A verdict that cannot be read
// must never come back as "supported": this check is what stands between a
// rewrite and an unapproved claim on the page.
func (c *Client) Check(ctx context.Context, document, claim string) (bool, error) {
	out, err := c.OL.Text(ctx, ollama.Request{
		Model: Model,
		// The model's own template wraps the prompt; this exact form is what its
		// card documents, so no system message of ours goes in front of it.
		User: "Document: " + document + "\nClaim: " + claim,
		// Fixed num_ctx keeps Ollama from reloading the model when a long
		// document arrives after a short one. Two tokens is "Yes"/"No" with slack.
		Options: ollama.Options{Temperature: 0, NumCtx: 4096, NumPredict: 4},
		Timeout: 60 * time.Second,
		Label:   "minicheck",
	})
	if err != nil {
		return false, err
	}
	return parse(out)
}

func parse(s string) (bool, error) {
	switch strings.ToLower(strings.Trim(strings.TrimSpace(s), ".!")) {
	case "yes":
		return true, nil
	case "no":
		return false, nil
	}
	return false, fmt.Errorf("minicheck: unreadable verdict %q", s)
}
