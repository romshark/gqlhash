package acceptance

import (
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// TestIgnoreModes covers -ignore, which decides how much of a document the hash
// covers and so what counts as the same document. It's the one flag that
// changes which requests are allowed.
func TestIgnoreModes(t *testing.T) {
	const (
		// The allowed document, and the same shape with another argument value
		// and with the value in a variable.
		otherValue = `{"query":"query GetUser{user(id:2){name}}"}`
		asVariable = `{"query":"query GetUser($id:Int){user(id:$id){name}}",` +
			`"variables":{"id":1}}`
	)

	each(t, func(t *testing.T, tgt target) {
		// The default covers everything, so another value is another document.
		t.Run("nothing", func(t *testing.T) {
			e := newEnv(t, tgt, []string{allowedDoc})
			if code, _ := post(t, e.server, docAllowed); code != http.StatusOK {
				t.Errorf("expected the document itself allowed; received %d", code)
			}
			for _, request := range []string{otherValue, asVariable} {
				if code, _ := post(t, e.server, request); code != http.StatusForbidden {
					t.Errorf("%s: expected 403; received %d", request, code)
				}
			}
		})

		// Input values are left out, so documents differing only in a value
		// share a hash. A variable is still a difference.
		t.Run("inputs", func(t *testing.T) {
			e := newEnv(t, tgt, []string{allowedDoc}, "-ignore", "inputs")
			for _, request := range []string{docAllowed, otherValue} {
				if code, _ := post(t, e.server, request); code != http.StatusOK {
					t.Errorf("%s: expected 200; received %d", request, code)
				}
			}
			if code, _ := post(t, e.server, asVariable); code != http.StatusForbidden {
				t.Errorf("expected the variable form refused; received %d", code)
			}
		})

		// Variables are left out on top of that, so the parameterized document
		// matches its inline-value equivalent.
		t.Run("variables", func(t *testing.T) {
			e := newEnv(t, tgt, []string{allowedDoc}, "-ignore", "variables")
			for _, request := range []string{docAllowed, otherValue, asVariable} {
				if code, _ := post(t, e.server, request); code != http.StatusOK {
					t.Errorf("%s: expected 200; received %d", request, code)
				}
			}
			// What's ignored is the values, not the shape.
			if code, _ := post(t, e.server, docRejected); code != http.StatusForbidden {
				t.Errorf("expected another selection refused; received %d", code)
			}
		})
	})
}

// TestHashFunctions covers -hash: the allowlist and the requests are hashed
// with the same function, so the decision is the same under every one of them.
// What the flag changes is the cost and the collision resistance, neither of
// which a client can see.
func TestHashFunctions(t *testing.T) {
	// The functions a proxy accepts. A hash that isn't collision resistant is
	// refused at startup, which the config tests cover.
	for _, name := range []string{"sha2", "sha3", "blake2b", "blake2s", "blake3"} {
		t.Run(name, func(t *testing.T) {
			each(t, func(t *testing.T, tgt target) {
				e := newEnv(t, tgt, []string{allowedDoc}, "-hash", name)
				if code, answer := post(t, e.server, docAllowed); code !=
					http.StatusOK {
					t.Errorf("expected the document allowed; received %d: %s",
						code, answer)
				}
				if code, _ := post(t, e.server, docRejected); code !=
					http.StatusForbidden {
					t.Errorf("expected 403; received %d", code)
				}
			})
		})
	}
}

// TestConcurrentReloads covers reloads arriving at once: the allowlist
// serializes them, so each answers for itself and none of them leaves the proxy
// serving nothing.
func TestConcurrentReloads(t *testing.T) {
	const concurrent = 8

	each(t, func(t *testing.T, tgt target) {
		e := shared(t, tgt)
		e.allow(t, allowedDoc)

		var wg sync.WaitGroup
		codes := make([]int, concurrent)
		for i := range concurrent {
			wg.Add(1)
			go func() {
				defer wg.Done()
				req, err := http.NewRequest(http.MethodPost,
					"http://"+e.control+"/reload", nil)
				if err != nil {
					return
				}
				res, err := http.DefaultClient.Do(req)
				if err != nil {
					return
				}
				defer func() { _ = res.Body.Close() }()
				codes[i] = res.StatusCode
			}()
		}
		wg.Wait()

		for i, code := range codes {
			if code != http.StatusOK {
				t.Errorf("reload %d: expected 200; received %d", i, code)
			}
		}
		// What was being served throughout is still being served.
		if code, _ := post(t, e.server, docAllowed); code != http.StatusOK {
			t.Errorf("expected the allowlist intact; received %d", code)
		}
	})
}

// TestDepthLimitFlag covers -depth-limit, which is the bound on what a document
// costs to hash. Nesting is what a document grows cheaply, so a deployment
// facing the open internet lowers it, and one serving a legitimately deep
// document raises it: without the flag neither could.
func TestDepthLimitFlag(t *testing.T) {
	// nested returns a document whose selection sets nest depth deep.
	nested := func(depth int) string {
		return "{" + strings.Repeat("f{", depth-1) + "f" + strings.Repeat("}", depth)
	}

	each(t, func(t *testing.T, tgt target) {
		// A document deeper than the default, which needs the flag raised for
		// the allowlist to take it at all.
		deep := nested(200)
		e := newEnv(t, tgt, []string{deep, "{ shallow }"}, "-depth-limit", "256")
		if code, answer := post(t, e.server,
			`{"query":`+strconv.Quote(deep)+`}`,
		); code != http.StatusOK {
			t.Errorf("expected the raised limit to serve it; received %d: %s",
				code, answer)
		}

		// Lowered, a document the default would take is refused, and the
		// allowlist can't hold it either.
		low := newEnv(t, tgt, []string{"{ shallow }"}, "-depth-limit", "3")
		if code, _ := post(t, low.server, `{"query":"{ shallow }"}`); code !=
			http.StatusOK {
			t.Errorf("expected a shallow document served; received %d", code)
		}
		if code, _ := post(t, low.server,
			`{"query":`+strconv.Quote(nested(4))+`}`,
		); code != http.StatusForbidden {
			t.Errorf("expected the lowered limit to refuse it; received %d", code)
		}

		// Below 1 is the default rather than no limit at all, so a document
		// past the default is still refused.
		off := newEnv(t, tgt, []string{"{ shallow }"}, "-depth-limit", "0")
		if code, _ := post(t, off.server,
			`{"query":`+strconv.Quote(nested(200))+`}`,
		); code != http.StatusForbidden {
			t.Errorf("expected 0 to take the default; received %d", code)
		}
	})
}
