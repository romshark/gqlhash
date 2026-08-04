package proxy

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestWriteErrorEscapes covers the envelope a rejection answers with. It's
// written as it is rather than marshalled, so what's under test is that a
// message carrying anything a JSON string can't hold still leaves a body the
// client can parse, and that it parses back to what went in.
func TestWriteErrorEscapes(t *testing.T) {
	for _, message := range []string{
		"malformed JSON",
		`a quote " in the middle`,
		`a backslash \ and a quote "`,
		"a newline \n a tab \t a return \r",
		"a control \x00 byte and a \x1f one",
		"a backspace \b and a form feed \f",
		`{"errors":[{"message":"an envelope of its own"}]}`,
		"a rune ✓ and an emoji 🔥",
		"",
	} {
		w := httptest.NewRecorder()
		writeError(w, http.StatusBadRequest, message, "BAD_REQUEST")

		var answer struct {
			Errors []struct {
				Message    string `json:"message"`
				Extensions struct {
					Code string `json:"code"`
				} `json:"extensions"`
			} `json:"errors"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &answer); err != nil {
			t.Errorf("%q: expected a body a client can parse: %v; received %s",
				message, err, w.Body)
			continue
		}
		if len(answer.Errors) != 1 {
			t.Errorf("%q: expected one error; received %s", message, w.Body)
			continue
		}
		if answer.Errors[0].Message != message {
			t.Errorf("expected the message back; sent %q, received %q",
				message, answer.Errors[0].Message)
		}
		if answer.Errors[0].Extensions.Code != "BAD_REQUEST" {
			t.Errorf("%q: expected the code; received %s", message, w.Body)
		}
	}
}

// TestWriteErrorNoAllocations pins that escaping costs a rejection nothing,
// which is the path a flood takes.
func TestWriteErrorNoAllocations(t *testing.T) {
	f := func(t *testing.T, name, message string) {
		t.Helper()
		run := func() { writeJSONString(io.Discard, message) }
		run()
		if n := testing.AllocsPerRun(200, run); n != 0 {
			t.Errorf("%s: expected no allocations; received %v", name, n)
		}
	}
	f(t, "plain", "operation not allowed")
	f(t, "escaped", "a quote \" a backslash \\ a control \x00")
}

// TestRejectEscapesTheError covers the one caller that answers with an error
// message rather than a constant, which is where an unescaped byte would arrive.
func TestRejectEscapesTheError(t *testing.T) {
	p, _ := testProxy(t, "{ a }")
	p.log = p.log.Output(io.Discard)

	// An error carrying a quote, the way a wrapped read failure could.
	w := httptest.NewRecorder()
	p.reject(w, http.StatusBadRequest,
		errors.New(`reading the request body: read "tcp": reset`).Error(),
		"BAD_REQUEST")

	var answer map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &answer); err != nil {
		t.Fatalf("expected a body a client can parse: %v; received %s", err, w.Body)
	}
	if !strings.Contains(w.Body.String(), `read \"tcp\"`) {
		t.Errorf("expected the quotes escaped; received %s", w.Body)
	}
}
