package proxy

import (
	"bytes"
	"errors"
	"unsafe"

	"github.com/romshark/jscan/v2"
)

var (
	// ErrNoQuery is a request body that holds no query member.
	ErrNoQuery = errors.New("no query")

	// ErrMalformedJSON is a request body that is no JSON.
	ErrMalformedJSON = errors.New("malformed JSON")

	// ErrBatch is a batch of requests where none is expected.
	ErrBatch = errors.New("batched request")

	// ErrInvalidEscape is a broken escape sequence in the query member.
	ErrInvalidEscape = errors.New("invalid escape sequence in query")
)

// span is the range of a document within the request body.
type span struct{ start, end int }

// extractJSON finds the query member of every request in body and appends its
// span to dst. body is one request object, or an array of them when batch is
// true. A span points at the raw JSON string contents, escapes included.
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
		if key := i.Key(); len(key) != len(`"query"`) || string(key) != `"query"` {
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
			return dst, ErrBatch
		}
		return dst, ErrMalformedJSON
	}
	if !found {
		return dst, ErrNoQuery
	}
	return dst, nil
}

// unescapeJSON returns the value of the JSON string contents s. Without an
// escape sequence it returns s itself, otherwise it appends the value to scratch
// and returns that.
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
			return nil, scratch, ErrInvalidEscape
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
				return nil, scratch, ErrInvalidEscape
			}
			v, ok := hex4(s[i+2:])
			if !ok {
				return nil, scratch, ErrInvalidEscape
			}
			i += 6
			if v >= 0xD800 && v <= 0xDBFF {
				// A leading surrogate takes a trailing one to make a rune.
				if i+6 > len(s) || s[i] != '\\' || s[i+1] != 'u' {
					return nil, scratch, ErrInvalidEscape
				}
				t, ok := hex4(s[i+2:])
				if !ok || t < 0xDC00 || t > 0xDFFF {
					return nil, scratch, ErrInvalidEscape
				}
				v = 0x10000 + (v-0xD800)<<10 + (t - 0xDC00)
				i += 6
			}
			scratch = appendRune(scratch, v)
		default:
			return nil, scratch, ErrInvalidEscape
		}
	}
	return scratch, scratch, nil
}

// extractQueryParam returns the percent-decoded value of the query parameter in
// rawQuery. With nothing to decode it returns a subslice of rawQuery.
func extractQueryParam(scratch []byte, rawQuery string) (value, newScratch []byte, err error) {
	for i := 0; i < len(rawQuery); {
		// One key=value pair, up to the next separator.
		end := i
		for end < len(rawQuery) && rawQuery[end] != '&' && rawQuery[end] != ';' {
			end++
		}
		pair := rawQuery[i:end]
		i = end + 1

		eq := 0
		for eq < len(pair) && pair[eq] != '=' {
			eq++
		}
		if eq == len(pair) || pair[:eq] != "query" {
			continue
		}
		raw := pair[eq+1:]
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
					return nil, scratch, ErrInvalidEscape
				}
				hi, ok1 := hexDigit(raw[j+1])
				lo, ok2 := hexDigit(raw[j+2])
				if !ok1 || !ok2 {
					return nil, scratch, ErrInvalidEscape
				}
				scratch = append(scratch, hi<<4|lo)
				j += 2
			default:
				scratch = append(scratch, raw[j])
			}
		}
		return scratch, scratch, nil
	}
	return nil, scratch, ErrNoQuery
}

func needsURLDecode(s string) bool {
	for i := range len(s) {
		if s[i] == '%' || s[i] == '+' {
			return true
		}
	}
	return false
}

// hex4 reads the four hexadecimal digits of a \uXXXX escape.
func hex4(s []byte) (uint32, bool) {
	var v uint32
	for i := range 4 {
		d, ok := hexDigit(s[i])
		if !ok {
			return 0, false
		}
		v = v<<4 | uint32(d)
	}
	return v, true
}

func hexDigit(b byte) (byte, bool) {
	switch {
	case b >= '0' && b <= '9':
		return b - '0', true
	case b >= 'a' && b <= 'f':
		return b - 'a' + 10, true
	case b >= 'A' && b <= 'F':
		return b - 'A' + 10, true
	}
	return 0, false
}

// appendRune appends v as UTF-8. An unpaired surrogate becomes U+FFFD, as it
// does in the standard library.
func appendRune(dst []byte, v uint32) []byte {
	switch {
	case v < 0x80:
		return append(dst, byte(v))
	case v < 0x800:
		return append(dst, byte(0xC0|v>>6), byte(0x80|v&0x3F))
	case v >= 0xD800 && v <= 0xDFFF:
		return append(dst, 0xEF, 0xBF, 0xBD)
	case v < 0x10000:
		return append(dst, byte(0xE0|v>>12), byte(0x80|v>>6&0x3F), byte(0x80|v&0x3F))
	default:
		return append(dst, byte(0xF0|v>>18), byte(0x80|v>>12&0x3F),
			byte(0x80|v>>6&0x3F), byte(0x80|v&0x3F))
	}
}

// unsafeBytes views s as a []byte without copying. The result is only read.
func unsafeBytes(s string) []byte {
	if len(s) == 0 {
		return nil
	}
	return unsafe.Slice(unsafe.StringData(s), len(s))
}
