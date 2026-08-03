package proxy

import (
	"bytes"
	"errors"
	"strings"
	"unicode/utf8"
	"unsafe"

	"github.com/romshark/jscan/v2"

	"github.com/romshark/gqlhash/v2/internal/unicodeesc"
)

var (
	errNoQuery       = errors.New("no query")
	errMalformedJSON = errors.New("malformed JSON")

	// errBatch is a batch of requests where none is expected.
	errBatch = errors.New("batched request")

	// errBatchTooLarge is a batch carrying more documents than -max-batch allows.
	errBatchTooLarge = errors.New("too many documents in the batch")

	errInvalidEscape  = errors.New("invalid escape sequence in query")
	errDuplicateQuery = errors.New("duplicate query parameter")
	errQueryCollision = errors.New(`naming collision on field "query"`)

	// errBodyOnGET is a GET carrying a body beside the query parameter its
	// document is read from.
	errBodyOnGET = errors.New("ambiguous request: a GET carrying a body")

	errQueryBesideBody = errors.New(
		"ambiguous request: a query parameter beside a body")
)

// span is the range of a document within the request body.
type span struct{ start, end int }

// extractJSON finds the query member of every request in body and appends its
// span to dst. body is one request object, or an array of them where maxBatch is
// 1 or more. A span points at the raw JSON string contents, escapes included.
//
// maxBatch is -max-batch: how many documents one request may carry,
// 0 for no batching at all, which makes an array [errBatch]. A batch past it is
// [errBatchTooLarge] and the scan stops there, so a body holding tens of
// thousands of documents costs the cap and not the body.
//
// A request object naming the query member twice is [errQueryCollision] rather
// than two documents: which one an API runs is the API's business,
// and checking one while the API runs the other allows a document nobody read.
// See [isQueryKey] for what counts as the same name.
//
// Nothing is copied: a span is a range within body.
func extractJSON(dst []span, body []byte, maxBatch int) ([]span, error) {
	// The member level of a lone request object, and one deeper within an array.
	level := 1

	// One word, since the callback runs per member and every variable it closes
	// over is a load and a store there.
	const (
		flagFound     = 1 << iota // A document has been found.
		flagSeen                  // This request object names the member.
		flagCollision             // It names it twice.
		flagTooMany               // The batch carries more than maxBatch.
	)
	var flags uint8

	errScan := jscan.Scan(body, func(i *jscan.Iterator[[]byte]) (err bool) {
		if i.Level() == 0 {
			// The outermost value decides whether this is a batch.
			if i.ValueType() == jscan.ValueTypeArray {
				if maxBatch < 1 {
					return true
				}
				level = 2
			}
			return false
		}
		// Each request of a batch is named once of its own.
		if level == 2 && i.Level() == 1 {
			flags &^= flagSeen
			return false
		}
		if i.Level() != level || !isQueryKey(i.Key()) {
			return false
		}
		if flags&flagSeen != 0 {
			flags |= flagCollision
			return true
		}
		flags |= flagSeen
		// Only a string is a document, but the name counts either way:
		// an API may still run something out of a member this reads nothing from,
		// so a second one beside it is a collision.
		if i.ValueType() != jscan.ValueTypeString {
			return false
		}
		// ValueIndex and ValueIndexEnd include the quotes.
		dst = append(dst, span{i.ValueIndex() + 1, i.ValueIndexEnd() - 1})
		flags |= flagFound
		// One past the cap is enough to refuse, so the rest of the array is never
		// read: a megabyte of documents costs what the cap allows plus one.
		// Only within an array — a lone request object is one document whatever
		// -max-batch says.
		if level == 2 && len(dst) > maxBatch {
			flags |= flagTooMany
			return true
		}
		return false
	})

	if errScan.IsErr() {
		if errScan.Code == jscan.ErrorCodeCallback {
			// The callback breaks for an unexpected batch, for a collision,
			// and for a batch past the cap.
			switch {
			case flags&flagCollision != 0:
				return dst, errQueryCollision
			case flags&flagTooMany != 0:
				return dst, errBatchTooLarge
			}
			return dst, errBatch
		}
		return dst, errMalformedJSON
	}
	if flags&flagFound == 0 {
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
// key carries the quotes around it, which this drops.
//
// The name is read the way a JSON decoder reads it, which makes the answer the
// same as the API's. [encoding/json] matches a struct field without case,
// so an API reading the body into one takes "queRY" for the query; and every decoder
// unescapes a key before matching, so "quer\u0079" is that member spelled
// another way. Reading only the exact spelling would leave
//
//	{"query":"<allowed>","quer\u0079":"<anything>"}
//
// checked against the first and executed as the second. Taking every spelling
// makes the second a collision, which [extractJSON] refuses — and an API that
// matches exactly runs nothing of that member, so refusing is conservative
// either way.
func isQueryKey(key []byte) bool {
	const want = "query"
	// Only \uXXXX spells a letter, so a name unescaping to want is five units
	// of one byte or six: every length from 5 to 30 in steps of five.
	// Any other length keeps an ordinary member off the path below.
	const escapedMax = len(`\u0071\u0075\u0065\u0072\u0079`)

	if len(key) < 2 {
		return false
	}
	name := key[1 : len(key)-1] // Without the quotes.
	if len(name) != len(want) {
		if len(name) < len(want) || len(name) > escapedMax ||
			(len(name)-len(want))%len(want) != 0 {
			return false
		}
		if bytes.IndexByte(name, '\\') < 0 {
			return false
		}
		// An escape costs a buffer, paid only by a name that could still be it.
		value, _, err := unescapeJSON(make([]byte, 0, escapedMax), name)
		if err != nil || len(value) != len(want) {
			return false
		}
		name = value
	}

	for i := range want {
		// Compared without case, the way a struct field is matched.
		// Only a letter of want lowercases onto one, so a backslash left in a name of
		// the plain length fails here rather than earlier.
		if name[i]|0x20 != want[i] {
			return false
		}
	}
	return true
}

// extractQueryParam returns the percent-decoded value of the query parameter
// in rawQuery. With nothing to decode it returns a subslice of rawQuery.
//
// A second query parameter is an error rather than a value to choose between.
// Which one an API runs is the API's business — [net/url.Values.Get] takes the first,
// other frameworks the last — so checking one while the API runs the
// other allows a document nobody read. The JSON body answers the same question
// the same way, see [extractJSON].
//
// A pair ends at ';' as well as at '&'. Go dropped ';' as a separator in 1.17,
// so a Go API sees no query where one is used while an API that still splits on
// it runs the document. The wider reading is the conservative half: a document
// refused here reaches nothing, one allowed here is what those APIs would run.
func extractQueryParam(scratch []byte, rawQuery string) (value, newScratch []byte, err error) {
	var raw string
	var found bool
	for i := 0; i < len(rawQuery); {
		end := i
		for end < len(rawQuery) && rawQuery[end] != '&' && rawQuery[end] != ';' {
			end++
		}
		pair := rawQuery[i:end]
		i = end + 1

		// A pair with no '=' is that key with an empty value,
		// as net/url reads it, so a valueless query counts as one.
		key, val := pair, ""
		eq := 0
		for eq < len(pair) && pair[eq] != '=' {
			eq++
		}
		if eq < len(pair) {
			key, val = pair[:eq], pair[eq+1:]
		}
		if !isQueryParam(key) {
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

// mergeQuery is the query string a forwarded request carries: the parameters of
// -upstream.url, then the client's verbatim. An endpoint that takes a parameter
// of its own — an API key, a project, an environment — is reached with it,
// where replacing the query outright would drop it and joining nothing would be a
// request the endpoint doesn't answer.
//
// The upstream's lead so an API reading the first of a repeated parameter reads
// what the deployment configured rather than what a client sent beside it.
// The client's are kept whole: a GET carries its document among them.
// The two can't collide over that document, since an -upstream.url naming a query
// parameter is refused at startup, see [hasQueryParam] and [Run].
//
// Nothing is allocated where the upstream has no query of its own,
// which is every deployment that names a plain endpoint.
func mergeQuery(upstream, client string) string {
	switch {
	case upstream == "":
		return client
	case client == "":
		return upstream
	}
	return upstream + "&" + client
}

// hasQueryParam reports whether rawQuery names the query parameter at all,
// which makes a request reading its document elsewhere ambiguous.
// It splits pairs the way [extractQueryParam] does, ';' included.
func hasQueryParam(rawQuery string) bool {
	for i := 0; i < len(rawQuery); {
		end := i
		for end < len(rawQuery) && rawQuery[end] != '&' && rawQuery[end] != ';' {
			end++
		}
		pair := rawQuery[i:end]
		i = end + 1

		key := pair
		if before, _, ok := strings.Cut(pair, "="); ok {
			key = before
		}
		if isQueryParam(key) {
			return true
		}
	}
	return false
}

// isQueryParam reports whether key names the query parameter, percent-decoded
// first. [net/url.ParseQuery] decodes a name before matching, so quer%79 is the
// query to the API behind this and has to be one here: read raw it would be
// another parameter, carrying a document this never checked.
// A broken escape is no name at all, as net/url reads it.
func isQueryParam(key string) bool {
	const name = "query"
	decoded := 0
	for i := 0; i < len(key); i++ {
		if decoded == len(name) {
			return false
		}
		c := key[i]
		switch c {
		case '+':
			c = ' '
		case '%':
			if i+2 >= len(key) || !unicodeesc.IsHexDigit(key[i+1]) ||
				!unicodeesc.IsHexDigit(key[i+2]) {
				return false
			}
			c = byte(unicodeesc.DigitValue(key[i+1])<<4 |
				unicodeesc.DigitValue(key[i+2]))
			i += 2
		}
		if c != name[decoded] {
			return false
		}
		decoded++
	}
	return decoded == len(name)
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
