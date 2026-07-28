package proxy

import (
	"bytes"
	"errors"
	"unicode/utf8"
	"unsafe"

	"github.com/romshark/jscan/v2"

	"github.com/romshark/gqlhash/v2/internal/unicodeesc"
)

var (
	// errNoQuery is a request body that holds no query member.
	errNoQuery = errors.New("no query")

	// errMalformedJSON is a request body that is no JSON.
	errMalformedJSON = errors.New("malformed JSON")

	// errBatch is a batch of requests where none is expected.
	errBatch = errors.New("batched request")

	// errInvalidEscape is a broken escape sequence in the query member.
	errInvalidEscape = errors.New("invalid escape sequence in query")

	// errDuplicateQuery is a request carrying the query parameter twice.
	errDuplicateQuery = errors.New("duplicate query parameter")
)

// span is the range of a document within the request body.
type span struct{ start, end int }

// extractJSON finds the query member of every request in body and appends
// its span to dst. body is one request object, or an array of them when batch is true.
// A span points at the raw JSON string contents, escapes included.
//
// Nothing is copied: a span is a range within body.
func extractJSON(dst []span, body []byte, batch bool) ([]span, error) {
	// The member level of a lone request object, and one deeper within an array.
	level := 1

	var found bool
	errScan := jscan.Scan(body, func(i *jscan.Iterator[[]byte]) (err bool) {
		if i.Level() == 0 {
			// The outermost value decides whether this is a batch.
			if i.ValueType() == jscan.ValueTypeArray {
				if !batch {
					return true
				}
				level = 2
			}
			return false
		}
		if i.Level() != level || i.ValueType() != jscan.ValueTypeString {
			return false
		}
		if !isQueryKey(i.Key()) {
			return false
		}
		// ValueIndex and ValueIndexEnd include the quotes.
		dst = append(dst, span{i.ValueIndex() + 1, i.ValueIndexEnd() - 1})
		found = true
		return false
	})

	if errScan.IsErr() {
		if errScan.Code == jscan.ErrorCodeCallback {
			// The callback only breaks for an unexpected batch.
			return dst, errBatch
		}
		return dst, errMalformedJSON
	}
	if !found {
		return dst, errNoQuery
	}
	return dst, nil
}

// unescapeJSON returns the value of the JSON string contents s.
// Without an escape sequence it returns s itself,
// otherwise it appends the value to scratch and returns that.
func unescapeJSON(scratch, s []byte) (value, newScratch []byte, err error) {
	i := bytes.IndexByte(s, '\\')
	if i < 0 {
		return s, scratch, nil
	}

	scratch = append(scratch[:0], s[:i]...)
	for i < len(s) {
		if s[i] != '\\' {
			scratch = append(scratch, s[i])
			i++
			continue
		}
		if i+1 >= len(s) {
			return nil, scratch, errInvalidEscape
		}
		switch s[i+1] {
		case '"', '\\', '/':
			scratch = append(scratch, s[i+1])
			i += 2
		case 'b':
			scratch = append(scratch, '\b')
			i += 2
		case 'f':
			scratch = append(scratch, '\f')
			i += 2
		case 'n':
			scratch = append(scratch, '\n')
			i += 2
		case 'r':
			scratch = append(scratch, '\r')
			i += 2
		case 't':
			scratch = append(scratch, '\t')
			i += 2
		case 'u':
			if i+6 > len(s) {
				return nil, scratch, errInvalidEscape
			}
			v, ok := unicodeesc.Hex4(s[i+2:])
			if !ok {
				return nil, scratch, errInvalidEscape
			}
			i += 6
			if unicodeesc.IsLeadingSurrogate(v) {
				// A leading surrogate takes a trailing one to make a rune.
				if i+6 > len(s) || s[i] != '\\' || s[i+1] != 'u' {
					return nil, scratch, errInvalidEscape
				}
				t, ok := unicodeesc.Hex4(s[i+2:])
				if !ok || !unicodeesc.IsTrailingSurrogate(t) {
					return nil, scratch, errInvalidEscape
				}
				v = unicodeesc.Pair(v, t)
				i += 6
			}
			scratch = utf8.AppendRune(scratch, rune(v))
		default:
			return nil, scratch, errInvalidEscape
		}
	}
	return scratch, scratch, nil
}

// isQueryKey reports whether key names the query member of a request object.
// key carries the quotes around it, which the comparison keeps.
//
// The letters are compared without case. [encoding/json] matches a struct field
// that way, so an API reading the body into one takes "queRY" for the query and
// runs it. Reading only the exact spelling would leave
//
//	{"query":"<allowed>","queRY":"<anything>"}
//
// checked against the first and executed as the second. An API that matches the
// name exactly reads no query in that member and runs nothing of it, so taking
// every spelling is the conservative reading for either.
func isQueryKey(key []byte) bool {
	const name = `"query"`
	if len(key) != len(name) {
		return false
	}
	for i := range len(name) {
		// The quotes have bit 0x20 set already, so they pass through unchanged.
		if key[i]|0x20 != name[i] {
			return false
		}
	}
	return true
}

// extractQueryParam returns the percent-decoded value of the query parameter
// in rawQuery. With nothing to decode it returns a subslice of rawQuery.
//
// A second query parameter is an error rather than a value to choose between.
// Which of them an API runs is the API's business: [net/url.Values.Get] takes
// the first, other frameworks take the last, and a proxy that checks one while
// the API runs the other has allowed a document it never read. The JSON body
// answers the same question by requiring every query member to be allowed.
//
// A pair ends at ';' as well as at '&'. Go dropped ';' as a separator in 1.17,
// so a Go API sees no query where one is used and answers an error of its own,
// while an API that still splits on it runs the document this checked. Reading
// it the wider way is the conservative half: a document refused here reaches
// nothing, and one allowed here is what any of those APIs would run.
func extractQueryParam(scratch []byte, rawQuery string) (value, newScratch []byte, err error) {
	var raw string
	var found bool
	for i := 0; i < len(rawQuery); {
		// One key=value pair, up to the next separator.
		end := i
		for end < len(rawQuery) && rawQuery[end] != '&' && rawQuery[end] != ';' {
			end++
		}
		pair := rawQuery[i:end]
		i = end + 1

		// A pair with no '=' is that key with an empty value, which is what
		// net/url makes of it, so a valueless query counts as one of them.
		key, val := pair, ""
		eq := 0
		for eq < len(pair) && pair[eq] != '=' {
			eq++
		}
		if eq < len(pair) {
			key, val = pair[:eq], pair[eq+1:]
		}
		if key != "query" {
			continue
		}
		if found {
			return nil, scratch, errDuplicateQuery
		}
		raw, found = val, true
	}
	if !found {
		return nil, scratch, errNoQuery
	}

	if !needsURLDecode(raw) {
		return unsafeBytes(raw), scratch, nil
	}
	scratch = scratch[:0]
	for j := 0; j < len(raw); j++ {
		switch raw[j] {
		case '+':
			scratch = append(scratch, ' ')
		case '%':
			if j+2 >= len(raw) {
				return nil, scratch, errInvalidEscape
			}
			if !unicodeesc.IsHexDigit(raw[j+1]) ||
				!unicodeesc.IsHexDigit(raw[j+2]) {
				return nil, scratch, errInvalidEscape
			}
			scratch = append(scratch, byte(unicodeesc.DigitValue(raw[j+1])<<4|
				unicodeesc.DigitValue(raw[j+2])))
			j += 2
		default:
			scratch = append(scratch, raw[j])
		}
	}
	return scratch, scratch, nil
}

func needsURLDecode(s string) bool {
	for i := range len(s) {
		if s[i] == '%' || s[i] == '+' {
			return true
		}
	}
	return false
}

// unsafeBytes views s as a []byte without copying. The result is only read.
func unsafeBytes(s string) []byte {
	if len(s) == 0 {
		return nil
	}
	return unsafe.Slice(unsafe.StringData(s), len(s))
}
