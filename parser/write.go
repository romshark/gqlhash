package parser

import (
	"io"
	"strings"
	"unicode/utf8"
)

// writer assembles the canonical token stream in buf and hands it to dst in one
// piece once the document is read. buf grows into whatever the largest document
// needs and is then reused.
//
// Why buffer instead of writing each token to dst: a hash consumes fixed-size
// blocks, 64 bytes for SHA-1, so many small writes keep it in its partial-block
// path, copying every fragment into its own buffer first. One large write lets
// its block loop run over buf directly. Token-by-token writes measure 38%
// slower for testdata/big.graphql with SHA-1 (2355 ns against 3256 ns), and no
// different with [io.Discard], so the Write calls themselves are not the cost.
type writer struct {
	dst io.Writer
	buf []byte

	// mute counts the reasons not to write: a Description and the sections that
	// [Options] marks as ignored.
	mute int
}

// flush hands the buffered stream to the destination and empties the buffer.
// It's called once per document.
//
// Every document writes at least the prefix of its first definition, so buf
// holds something.
func (w *writer) flush() error {
	_, err := w.dst.Write(w.buf)
	w.buf = w.buf[:0]
	return err
}

// pref writes a single byte, which is one of the HPref prefixes or a punctuator
// of a type reference.
func (w *writer) pref(b byte) {
	if w.mute == 0 {
		w.buf = append(w.buf, b)
	}
}

// str writes s as it is.
func (w *writer) str(s string) {
	if w.mute == 0 {
		w.buf = append(w.buf, s...)
	}
}

// tok writes prefix followed by s, the common case of a prefix introducing a
// name.
func (w *writer) tok(prefix byte, s string) {
	if w.mute != 0 {
		return
	}
	w.buf = append(append(w.buf, prefix), s...)
}

// nameTok writes prefix followed by the Name that begins at s[i], which must be
// a NameStart, and returns the index right after that Name.
//
// Why scan and write in one step: a Name is the most frequent token of a
// document, and this keeps it down to a single call.
// Reference:
//
//   - https://spec.graphql.org/September2025/#Name
func (w *writer) nameTok(prefix byte, s string, i int) int {
	start := i
	i++
	for i+8 <= len(s) {
		if !lutNameCont[s[i]] {
			goto WRITE
		}
		if !lutNameCont[s[i+1]] {
			i += 1
			goto WRITE
		}
		if !lutNameCont[s[i+2]] {
			i += 2
			goto WRITE
		}
		if !lutNameCont[s[i+3]] {
			i += 3
			goto WRITE
		}
		if !lutNameCont[s[i+4]] {
			i += 4
			goto WRITE
		}
		if !lutNameCont[s[i+5]] {
			i += 5
			goto WRITE
		}
		if !lutNameCont[s[i+6]] {
			i += 6
			goto WRITE
		}
		if !lutNameCont[s[i+7]] {
			i += 7
			goto WRITE
		}
		i += 8
	}
	for i < len(s) && lutNameCont[s[i]] {
		i++
	}
WRITE:
	w.tok(prefix, s[start:i])
	return i
}

// nameStr is [writer.nameTok] for a Name that no prefix introduces.
func (w *writer) nameStr(s string, i int) int {
	start := i
	i = nameEnd(s, i+1)
	w.str(s[start:i])
	return i
}

// esc writes b as a backslash escape. No string value can hold a byte that
// looks like a hash prefix.
func (w *writer) esc(b byte) {
	if w.mute == 0 {
		w.buf = append(w.buf, lutStringEscapeSeq[b][0], lutStringEscapeSeq[b][1])
	}
}

// strByte writes one byte of a string value, escaping it if it must not appear
// as it is.
func (w *writer) strByte(b byte) {
	if lutStringEscape[b] {
		w.esc(b)
		return
	}
	w.pref(b)
}

// strRune writes r as UTF-8, byte by byte, escaping the bytes that need it.
func (w *writer) strRune(r rune) {
	var b [utf8.UTFMax]byte
	n := utf8.EncodeRune(b[:], r)
	for _, c := range b[:n] {
		w.strByte(c)
	}
}

// stringValue writes a single-line string with its escape sequences evaluated,
// so two spellings of one value produce the same bytes. s is the source between
// the quotes as [scanStringLine] validated it, esc says whether it holds any
// escape sequence.
// Reference:
//
//   - https://spec.graphql.org/September2025/#sec-String-Value
func (w *writer) stringValue(s string, esc bool) {
	if !esc {
		// Without an escape sequence the value stands for itself. A control
		// byte, the only other byte that needs escaping, is rejected in a
		// single-line string.
		w.str(s)
		return
	}
	for {
		i := strings.IndexByte(s, '\\')
		if i < 0 {
			w.str(s)
			return
		}
		w.str(s[:i])
		s = s[i:]

		switch s[1] {
		case 'b':
			w.strByte('\b')
			s = s[2:]
		case 'f':
			w.strByte('\f')
			s = s[2:]
		case 'n':
			w.strByte('\n')
			s = s[2:]
		case 'r':
			w.strByte('\r')
			s = s[2:]
		case 't':
			w.strByte('\t')
			s = s[2:]
		case 'u':
			var v uint32
			if s[2] == '{' {
				// Variable-width `\u{HexDigit+}`. Leading zeroes can't overflow v,
				// the value is at most 0x10FFFF.
				i := 3
				for ; s[i] != '}'; i++ {
					v = v<<4 | hexByteValue(s[i])
				}
				s = s[i+1:]
			} else {
				v = fixedWidthEscapeValue(s[2:])
				s = s[6:]
				if isLeadingSurrogate(v) {
					// A surrogate pair spells out a single code point.
					trailing := fixedWidthEscapeValue(s[2:])
					v = 0x10000 + (v-0xD800)<<10 + (trailing - 0xDC00)
					s = s[6:]
				}
			}
			w.strRune(rune(v))
		default:
			// `\"`, `\\` and `\/` stand for the character itself.
			w.strByte(s[1])
			s = s[2:]
		}
	}
}

// blockStringValue writes the BlockStringValue of s, the raw content between the
// `"""` delimiters as [scanStringBlock] validated it. prefixLen bytes of common
// indentation are stripped from every line but the first, leading and trailing
// blank lines are dropped, and the lines are joined by a single line feed. LF,
// CRLF and CR therefore produce the same bytes.
// Reference:
//
//   - https://spec.graphql.org/September2025/#BlockStringValue()
func (w *writer) blockStringValue(s string, prefixLen int) {
	s = trimEmptyLinesSuffix(s)

	// The first line keeps its indentation, the common prefix never applies to it.
	i, firstLineIsEmpty := 0, true
	for ; i < len(s) && lineTerminatorLen(s, i) == 0; i++ {
		if s[i] != ' ' && s[i] != '\t' {
			firstLineIsEmpty = false
		}
	}
	contentSeen, firstLine := !firstLineIsEmpty, true
	if !firstLineIsEmpty {
		w.blockStringLine(s[:i])
		firstLine = false
	}

	for i < len(s) {
		i += lineTerminatorLen(s, i)

		// Strip up to prefixLen leading whitespace bytes.
		for skipped := 0; i < len(s) && skipped < prefixLen &&
			isWhiteSpace(s[i]); skipped++ {
			i++
		}
		lineStart, blank := i, true
		for ; i < len(s) && lineTerminatorLen(s, i) == 0; i++ {
			if !isWhiteSpace(s[i]) {
				blank = false
			}
		}
		if blank && !contentSeen {
			// A leading blank line is dropped. Trailing ones are gone already,
			// so a blank line after content is interior and stays.
			continue
		}
		if !blank {
			contentSeen = true
		}
		if !firstLine {
			w.pref('\n')
		}
		firstLine = false
		w.blockStringLine(s[lineStart:i])
	}
}

// blockStringLine writes one line of a block string. Its only escape is `\"""`,
// which stands for `"""`.
// Reference:
//
//   - https://spec.graphql.org/September2025/#BlockStringCharacter
func (w *writer) blockStringLine(s string) {
	const escapedTripleQuote = `\"""`
	start := 0
	for i := 0; i < len(s); i++ {
		if !lutStringEscape[s[i]] {
			continue
		}
		w.str(s[start:i])
		if hasPrefixAt(s, i, escapedTripleQuote) {
			w.str(`"""`)
			i += len(escapedTripleQuote) - 1 // The loop skips the last byte.
		} else {
			w.esc(s[i])
		}
		start = i + 1
	}
	w.str(s[start:])
}

// trimEmptyLinesSuffix removes any trailing empty lines from s.
// An empty line is a line that holds nothing but WhiteSpace.
//
// Requirement: s holds at least one byte that is neither WhiteSpace nor a
// LineTerminator, which is what [scanStringBlock] reports as content.
func trimEmptyLinesSuffix(s string) string {
	e := len(s)
	for {
		// The LineTerminator that ends the second-to-last line. Scanning for its
		// last byte keeps CRLF intact: '\n' is found first, the preceding '\r'
		// is picked up below.
		termEnd := -1
		for i := e - 1; i >= 0; i-- {
			if s[i] == '\n' || s[i] == '\r' {
				termEnd = i
				break
			}
		}
		if termEnd < 0 {
			// Only one line left, and it holds the content.
			return s[:e]
		}
		if !containsOnlyWhiteSpace(s[termEnd+1 : e]) {
			return s[:e] // Line is not empty, stop trimming.
		}
		if s[termEnd] == '\n' && termEnd > 0 && s[termEnd-1] == '\r' {
			termEnd-- // Drop the whole CRLF, not just the '\n'.
		}
		e = termEnd // Remove empty line.
	}
}

func containsOnlyWhiteSpace(s string) bool {
	for i := 0; i < len(s); i++ {
		if !isWhiteSpace(s[i]) {
			return false
		}
	}
	return true
}

// fixedWidthEscapeValue returns the value of the four hexadecimal digits of a
// fixed-width `\uXXXX` escape, which s must begin with.
func fixedWidthEscapeValue(s string) uint32 {
	return hexByteValue(s[0])<<12 | hexByteValue(s[1])<<8 |
		hexByteValue(s[2])<<4 | hexByteValue(s[3])
}

// hexByteValue returns the numeric value of hexadecimal digit b.
// The result is meaningless unless lutHex[b] is true.
func hexByteValue(b byte) uint32 {
	switch {
	case b >= '0' && b <= '9':
		return uint32(b - '0')
	case b >= 'a' && b <= 'f':
		return uint32(b-'a') + 10
	}
	return uint32(b-'A') + 10
}

// isUnicodeScalarValue returns true if v is a Unicode scalar value. The
// surrogate code points 0xD800-0xDFFF are not.
// Reference:
//
//   - https://spec.graphql.org/September2025/#sec-Unicode
func isUnicodeScalarValue(v uint32) bool {
	return v <= 0xD7FF || (v >= 0xE000 && v <= 0x10FFFF)
}

// isLeadingSurrogate returns true if v is a Leading Surrogate code point.
// Reference:
//
//   - https://spec.graphql.org/September2025/#StringCharacter
func isLeadingSurrogate(v uint32) bool { return v >= 0xD800 && v <= 0xDBFF }

// isTrailingSurrogate returns true if v is a Trailing Surrogate code point.
// Reference:
//
//   - https://spec.graphql.org/September2025/#StringCharacter
func isTrailingSurrogate(v uint32) bool { return v >= 0xDC00 && v <= 0xDFFF }
