package acceptance

import (
	"net/http"
	"strings"
	"testing"
)

// TestEnvNames covers the mapping from a flag to the variable that sets it:
// the prefix, and the name with dashes and dots as underscores.
// A deployment configures a container this way and has no command line to fall back on.
func TestEnvNames(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		dir := t.TempDir()
		writeDoc(t, dir, "a.graphql", allowedDoc)
		api := newAPI(t)

		// A dotted flag, through the environment alone.
		t.Setenv("GQLHASH_PROXY_SERVER_MAX_BODY", "128")
		s := serve(t, tgt, "-upstream.url", api.URL+"/graphql", "-allowlist", dir)

		body := `{"query":"` + allowedText + `","x":"` +
			strings.Repeat("p", 256) + `"}`
		if code, answer := post(t, s, body); code !=
			http.StatusRequestEntityTooLarge {
			t.Errorf("expected the variable to set the limit; received %d: %s",
				code, answer)
		}
		if code, _ := post(t, s, docAllowed); code != http.StatusOK {
			t.Error("expected a request under the limit to be served")
		}
	})
}

// TestEnvCommandLineWins covers the precedence:
// a flag given both ways is the one on the command line.
// A deployment that overrides a container's environment for one run has to be able to.
func TestEnvCommandLineWins(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		dir := t.TempDir()
		writeDoc(t, dir, "a.graphql", allowedDoc)
		api := newAPI(t)

		// The environment says one thing and the flag another.
		t.Setenv("GQLHASH_PROXY_SERVER_MAX_BODY", "16")
		s := serve(t, tgt, "-upstream.url", api.URL+"/graphql", "-allowlist", dir,
			"-server.max-body", "4096")

		if code, answer := post(t, s, docAllowed); code != http.StatusOK {
			t.Errorf("expected the flag to win; received %d: %s", code, answer)
		}
	})
}

// TestEnvRejectsBadValues covers a variable a flag can't take: it's the same
// refusal as on the command line, so a typo in a deployment stops the container
// rather than starting one that serves something else.
func TestEnvRejectsBadValues(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		dir := t.TempDir()
		writeDoc(t, dir, "a.graphql", allowedDoc)
		args := []string{
			"-upstream.url", "http://127.0.0.1:1/graphql", "-allowlist", dir,
			"-server.listen", "127.0.0.1:0", "-control.listen", "127.0.0.1:0",
		}

		for _, tc := range []struct{ name, env string }{
			{"a number that isn't one", "GQLHASH_PROXY_SERVER_MAX_BODY=lots"},
			{"a duration that isn't one", "GQLHASH_PROXY_UPSTREAM_TIMEOUT=soon"},
			{"a mode nobody has", "GQLHASH_PROXY_IGNORE=bogus"},
			{"a hash that forges", "GQLHASH_PROXY_HASH=md5"},
			// A rule between two flags holds however they were given:
			// this one is a write timeout under -upstream.timeout,
			// which would cut every forward short.
			{"a combination the flags refuse",
				"GQLHASH_PROXY_SERVER_WRITE_TIMEOUT=5s"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				code, _, stderr := runWithEnv(t, tgt, []string{tc.env}, args...)
				if code != exitBadArgs {
					t.Errorf("expected %d; received %d: %s", exitBadArgs, code, stderr)
				}
				if strings.TrimSpace(stderr) == "" {
					t.Error("expected a reason on stderr")
				}
			})
		}
	})
}

// TestEnvTokenIsTrimmed covers the token as a secret store hands it over.
// A file mounted by Kubernetes ends in a newline more often than not,
// and no HTTP header can carry one:
// a token kept untrimmed is a control server nobody can reach.
func TestEnvTokenIsTrimmed(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		dir := t.TempDir()
		writeDoc(t, dir, "a.graphql", allowedDoc)
		api := newAPI(t)

		t.Setenv(controlTokenEnv, "s3cret\n")
		s := serve(t, tgt, "-upstream.url", api.URL+"/graphql", "-allowlist", dir)

		if code, body := control(
			t, s, http.MethodPost, "/reload", "s3cret",
		); code != http.StatusOK {
			t.Errorf("expected the trimmed token to authorize; received %d: %s",
				code, body)
		}
		// It's still a token: the wrong one is still refused.
		if code, _ := control(
			t, s, http.MethodPost, "/reload", "wrong",
		); code != http.StatusUnauthorized {
			t.Errorf("expected 401 for a wrong token; received %d", code)
		}
	})
}
