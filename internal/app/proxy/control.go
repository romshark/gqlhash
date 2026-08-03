package proxy

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/rs/zerolog"

	"github.com/romshark/gqlhash/v2/internal/allowlist"
)

// control serves the endpoints that change what the proxy does.
// -control.listen must name an address no client of the API can reach:
// a reload rereads and reparses every document.
type control struct {
	allowlist *allowlist.Allowlist

	// dir is what a reload reads, and what the log of one names.
	dir   string
	proxy *proxy
	token string
	log   zerolog.Logger
}

// routes adds the control endpoints to mux, which also serves the metrics.
func (c *control) routes(mux *http.ServeMux) {
	mux.HandleFunc("/reload", c.reload)
	mux.HandleFunc("/status", c.status)
	mux.HandleFunc("/healthz", c.healthz)
}

// healthz answers 200 while the proxy serves, for a liveness probe.
//
// It computes nothing: [build] fails the start where the allowlist can't load,
// and the data plane serves before this listener exists, so a 503 would take no request.
// A probe reads the shutdown instead — the control server closes
// before the data plane drains, which is the order an eviction wants.
//
// A load that skipped files isn't reported here: one bad document would take
// every replica out of service. /status and the metrics carry it.
func (c *control) healthz(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "healthz takes GET or HEAD", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = io.WriteString(w, "ok\n")
}

// status answers with the state of the allowlist and the decisions made.
// No token: it changes nothing and says nothing a metrics scrape doesn't.
func (c *control) status(w http.ResponseWriter, _ *http.Request) {
	documents, loadedAt := c.allowlist.Stats()
	d := c.proxy.snapshot()
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = fmt.Fprintf(w,
		`{"documents":%d,"loaded_at":%q,"allowed":%d,"rejected":%d,`+
			`"malformed":%d,"too_large":%d,"ambiguous":%d,"too_deep":%d,`+
			`"batch_too_large":%d,"method_not_allowed":%d,`+
			`"upstream_errors":%d}`+"\n",
		documents, loadedAt.Format(time.RFC3339), d.allowed, d.rejected,
		d.malformed, d.tooLarge, d.ambiguous, d.tooDeep, d.batchBig, d.methodBad,
		d.upstream)
}

// reload rereads the allowlist and answers with what it holds afterwards.
//
// Only POST does it, so a browser or a scraper that wanders onto the address
// can't spend the work of a reload.
func (c *control) reload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "reload takes POST", http.StatusMethodNotAllowed)
		return
	}
	if !c.authorized(r) {
		w.Header().Set("WWW-Authenticate", `Bearer realm="gqlhash-proxy"`)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	// Load serializes its callers, so concurrent requests queue instead of
	// parsing the same directory at once.
	result, err := c.allowlist.Reload(c.dir)
	if err != nil {
		c.log.Error().Err(err).Msg("reloading the allowlist")
		http.Error(w, "reloading the allowlist failed", http.StatusInternalServerError)
		return
	}
	logReload(c.log, c.dir, result)

	// A skipped file is no failure of the reload — the rest is published — but
	// it's answered, so a deployment can fail on it without reading the log.
	// Files is never nil: a load that took nothing answers [] and not null.
	var answer reloadAnswer
	answer.Documents.Total = len(result.Files)
	answer.Documents.Files = result.Files
	skipped := result.Skipped
	if result.SchemaErr != nil {
		skipped = append([]error{result.SchemaErr}, skipped...)
	}
	answer.Skipped.Total = len(skipped)
	answer.Skipped.Errors = make([]string, len(skipped))
	for i, e := range skipped {
		answer.Skipped.Errors[i] = e.Error()
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if err := json.NewEncoder(w).Encode(answer); err != nil {
		// On its way out, so there's nothing left to say to the client.
		c.log.Debug().Err(err).Msg("writing the reload answer")
	}
}

// reloadAnswer is what a reload replies: what the allowlist holds now and what
// didn't make it. Every error names the file, the line and the column.
//
// Exported fields on an unexported type: encoding/json marshals no other kind,
// and lowercasing them answers {} to every reload.
type reloadAnswer struct {
	Documents struct {
		Total int      `json:"total"`
		Files []string `json:"files"`
	} `json:"documents"`
	Skipped struct {
		Total  int      `json:"total"`
		Errors []string `json:"errors"`
	} `json:"skipped"`
}

// authorized reports whether r carries the token.
// Every request passes when none is configured.
func (c *control) authorized(r *http.Request) bool {
	if c.token == "" {
		return true
	}
	// The scheme is matched without case, as RFC 7235 defines it.
	// What follows is the token, matched exactly.
	authorization := r.Header.Get("Authorization")
	const scheme = "bearer "
	if len(authorization) < len(scheme) ||
		!strings.EqualFold(authorization[:len(scheme)], scheme) {
		return false
	}
	given := authorization[len(scheme):]
	// A constant-time compare, so a wrong token says nothing about the right one.
	return subtle.ConstantTimeCompare([]byte(given), []byte(c.token)) == 1
}
