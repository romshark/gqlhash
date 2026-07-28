package proxy

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/rs/zerolog"

	"github.com/romshark/gqlhash/v2/internal/allowlist"
)

// control serves the endpoints that change what the proxy does. It's reachable
// only where -control.listen names an address, which must be one no client of the API
// can reach: a reload rereads and reparses every document.
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
}

// status answers with the state of the allowlist and the decisions made.
// It takes no token: it changes nothing and says nothing a scrape of the metrics doesn't.
func (c *control) status(w http.ResponseWriter, _ *http.Request) {
	documents, loadedAt := c.allowlist.Stats()
	d := c.proxy.snapshot()
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = fmt.Fprintf(w,
		`{"documents":%d,"loaded_at":%q,"allowed":%d,"rejected":%d,`+
			`"malformed":%d,"upstream_errors":%d}`+"\n",
		documents, loadedAt.Format(time.RFC3339), d.allowed, d.rejected,
		d.malformed, d.upstream)
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

	// A skipped file is no failure of the reload: the rest is published.
	// It's answered so a deployment can fail on it without reading the log.
	// Files is never nil, so a load that took nothing answers with an empty
	// list rather than a null a client has to special-case.
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
		// The answer is on its way out, so there's nothing left to say to the client.
		c.log.Debug().Err(err).Msg("writing the reload answer")
	}
}

// reloadAnswer is what a reload replies: what the allowlist holds now and what
// didn't make it. Every error names the file, the line and the column.
//
// The fields are exported although the type isn't: encoding/json marshals no
// other kind, and lowercasing them answers {} to every reload.
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
	given, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok {
		return false
	}
	// A constant-time compare, so a wrong token says nothing about the right one.
	return subtle.ConstantTimeCompare([]byte(given), []byte(c.token)) == 1
}
