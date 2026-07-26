package parser

import "unicode/utf8"

// bom is the UTF-8 encoding of UnicodeBOM (U+FEFF). bomFirstByte is its first
// byte, which [skipIgnorables] dispatches on.
// Reference:
//
//   - https://spec.graphql.org/September2025/#UnicodeBOM
const (
	bom          = "\xef\xbb\xbf"
	bomFirstByte = '\xef'
)

// isWhiteSpace returns true if b is a WhiteSpace.
// Reference:
//
//   - https://spec.graphql.org/September2025/#WhiteSpace
func isWhiteSpace(b byte) bool { return b == ' ' || b == '\t' }

// lineTerminatorLen returns the length in bytes of the LineTerminator at s[i],
// or 0 if s[i] doesn't begin one. CRLF is a single LineTerminator, hence 2.
// Reference:
//
//   - https://spec.graphql.org/September2025/#LineTerminator
func lineTerminatorLen(s string, i int) int {
	switch s[i] {
	case '\n':
		return 1
	case '\r':
		if i+1 < len(s) && s[i+1] == '\n' {
			return 2
		}
		return 1
	}
	return 0
}

// hasPrefixAt reports whether s continues with prefix at index i.
func hasPrefixAt(s string, i int, prefix string) bool {
	return len(s)-i >= len(prefix) && s[i:i+len(prefix)] == prefix
}

// isKeywordAt reports whether the whole word kw begins at s[i]. A longer name
// doesn't match: the enum "nullable" is no "null".
// Reference:
//
//   - https://spec.graphql.org/September2025/#Name
func isKeywordAt(s string, i int, kw string) bool {
	if len(s)-i < len(kw) || s[i:i+len(kw)] != kw {
		return false
	}
	i += len(kw)
	return i == len(s) || !lutNameCont[s[i]]
}

// sourceCharacterLen returns the length in bytes of the SourceCharacter at s[i],
// or 0 if those bytes aren't a well-formed UTF-8 encoding of a Unicode scalar
// value. Surrogates, overlong encodings, truncated sequences and values above
// U+10FFFF all return 0.
//
// Requirement: s[i] is at least [utf8.RuneSelf]. An ASCII byte is a
// SourceCharacter one byte wide and no scanner asks.
// Reference:
//
//   - https://spec.graphql.org/September2025/#SourceCharacter
func sourceCharacterLen(s string, i int) int {
	// DecodeRuneInString reports every malformed encoding as (RuneError, 1).
	// A literal U+FFFD decodes to 3 bytes and is told apart by the size.
	if r, size := utf8.DecodeRuneInString(s[i:]); r != utf8.RuneError || size > 1 {
		return size
	}
	return 0
}

// nameEnd returns the index of the first byte at or after i that is no
// NameContinue.
// Reference:
//
//   - https://spec.graphql.org/September2025/#NameContinue
func nameEnd(s string, i int) int {
	for i+8 <= len(s) {
		if !lutNameCont[s[i]] {
			return i
		}
		if !lutNameCont[s[i+1]] {
			return i + 1
		}
		if !lutNameCont[s[i+2]] {
			return i + 2
		}
		if !lutNameCont[s[i+3]] {
			return i + 3
		}
		if !lutNameCont[s[i+4]] {
			return i + 4
		}
		if !lutNameCont[s[i+5]] {
			return i + 5
		}
		if !lutNameCont[s[i+6]] {
			return i + 6
		}
		if !lutNameCont[s[i+7]] {
			return i + 7
		}
		i += 8
	}
	for i < len(s) && lutNameCont[s[i]] {
		i++
	}
	return i
}

// skipIgnorables returns the index of the first byte at or after i that isn't
// part of an Ignored token.
//
// A byte inside a comment that is no CommentChar, which is any byte that breaks
// UTF-8, ends the skip. The caller reports it as an unexpected token.
// Reference:
//
//   - https://spec.graphql.org/September2025/#sec-Line-Terminators
//   - https://spec.graphql.org/September2025/#sec-Comments
//   - https://spec.graphql.org/September2025/#sec-White-Space
//   - https://spec.graphql.org/September2025/#UnicodeBOM
func skipIgnorables(s string, i int) int {
	// Why split in two: most tokens of a minified document have nothing to skip,
	// and this body stays small enough to be inlined into the state machine.
	if i < len(s) && lutIgnorable[s[i]] != ignorableNone {
		return skipIgnorablesSlow(s, i)
	}
	return i
}

// skipIgnorablesSlow skips one or more Ignored tokens, including those wider
// than one byte: a Comment and a UnicodeBOM.
func skipIgnorablesSlow(s string, i int) int {
	for {
		// WhiteSpace, LineTerminators and commas, which are Ignored as well.
		for i+8 <= len(s) {
			if lutIgnorable[s[i]] != ignorableByte {
				goto DISPATCH
			}
			if lutIgnorable[s[i+1]] != ignorableByte {
				i += 1
				goto DISPATCH
			}
			if lutIgnorable[s[i+2]] != ignorableByte {
				i += 2
				goto DISPATCH
			}
			if lutIgnorable[s[i+3]] != ignorableByte {
				i += 3
				goto DISPATCH
			}
			if lutIgnorable[s[i+4]] != ignorableByte {
				i += 4
				goto DISPATCH
			}
			if lutIgnorable[s[i+5]] != ignorableByte {
				i += 5
				goto DISPATCH
			}
			if lutIgnorable[s[i+6]] != ignorableByte {
				i += 6
				goto DISPATCH
			}
			if lutIgnorable[s[i+7]] != ignorableByte {
				i += 7
				goto DISPATCH
			}
			i += 8
		}
		for ; i < len(s); i++ {
			if lutIgnorable[s[i]] != ignorableByte {
				goto DISPATCH
			}
		}
		return i

	DISPATCH:
		switch s[i] {
		case '#':
			// A CommentChar is a SourceCharacter but no LineTerminator
			// (https://spec.graphql.org/September2025/#CommentChar). The comment
			// ends at the line break, which the loop above skips.
			i++
			for {
				for i+8 <= len(s) {
					if lutCommentStop[s[i]] {
						goto COMMENT_STOP
					}
					if lutCommentStop[s[i+1]] {
						i += 1
						goto COMMENT_STOP
					}
					if lutCommentStop[s[i+2]] {
						i += 2
						goto COMMENT_STOP
					}
					if lutCommentStop[s[i+3]] {
						i += 3
						goto COMMENT_STOP
					}
					if lutCommentStop[s[i+4]] {
						i += 4
						goto COMMENT_STOP
					}
					if lutCommentStop[s[i+5]] {
						i += 5
						goto COMMENT_STOP
					}
					if lutCommentStop[s[i+6]] {
						i += 6
						goto COMMENT_STOP
					}
					if lutCommentStop[s[i+7]] {
						i += 7
						goto COMMENT_STOP
					}
					i += 8
				}
				for ; i < len(s); i++ {
					if lutCommentStop[s[i]] {
						goto COMMENT_STOP
					}
				}
				return i

			COMMENT_STOP:
				if s[i] == '\n' || s[i] == '\r' {
					break // End of the comment.
				}
				// A multi-byte SourceCharacter, or no SourceCharacter at all.
				n := sourceCharacterLen(s, i)
				if n == 0 {
					return i // Malformed UTF-8 ends the comment.
				}
				i += n
			}
		case bomFirstByte:
			// UnicodeBOM (U+FEFF) may appear before or after any token, not just
			// at the start of the document.
			if !hasPrefixAt(s, i, bom) {
				return i
			}
			i += len(bom)
		default:
			return i
		}
	}
}

// scanNumber scans the IntValue or FloatValue that begins at s[i], which must be
// a NegativeSign or a Digit. It returns the index right after the number and
// whether it's a FloatValue.
//
// A Digit, '.' or NameStart may not follow the number: a number that breaks a
// lexical rule is one broken token, not several valid ones.
// Reference:
//
//   - https://spec.graphql.org/September2025/#IntValue
//   - https://spec.graphql.org/September2025/#FloatValue
func scanNumber(s string, i int) (end int, isFloat bool, errPos int, err error) {
	start := i

	// IntegerPart (https://spec.graphql.org/September2025/#IntegerPart).
	if s[i] == '-' {
		// A NegativeSign needs a Digit after it.
		i++
	}
	if i >= len(s) || !lutDigit[s[i]] {
		return i, false, start, ErrMalformedNumber
	}
	if s[i] == '0' {
		// A Digit after the zero is a leading zero, which the number end rejects.
		i++
	} else {
		for i++; i < len(s) && lutDigit[s[i]]; i++ {
		}
	}

	if i < len(s) && (s[i] == '.' || s[i] == 'e' || s[i] == 'E') {
		// FloatValue (https://spec.graphql.org/September2025/#sec-Float-Value).
		isFloat = true
		if s[i] == '.' {
			// FractionalPart.
			i++
			start = i
			for ; i < len(s) && lutDigit[s[i]]; i++ {
			}
			if i == start {
				return i, isFloat, i, ErrMalformedNumber // No digit after the dot.
			}
		}
		if i < len(s) && (s[i] == 'e' || s[i] == 'E') {
			// ExponentPart, which may be signed.
			i++
			if i < len(s) && (s[i] == '-' || s[i] == '+') {
				i++
			}
			start = i
			for ; i < len(s) && lutDigit[s[i]]; i++ {
			}
			if i == start {
				// No digit after the exponent indicator.
				return i, isFloat, i, ErrMalformedNumber
			}
		}
	}

	if i < len(s) && (lutDigit[s[i]] || s[i] == '.' || lutNameStart[s[i]]) {
		return i, isFloat, i, ErrMalformedNumber
	}
	return i, isFloat, 0, nil
}

// scanStringLine scans the contents of a single-line StringValue. i must be the
// index right after the opening '"'. It returns the index right after the
// closing '"' and whether the value holds any escape sequence.
// Reference:
//
//   - https://spec.graphql.org/September2025/#sec-String-Value
func scanStringLine(s string, i int) (end int, esc bool, errPos int, err error) {
	for {
		// StringCharacters that stand for themselves.
		for i+8 <= len(s) {
			if lutStringStop[s[i]] {
				goto DISPATCH
			}
			if lutStringStop[s[i+1]] {
				i += 1
				goto DISPATCH
			}
			if lutStringStop[s[i+2]] {
				i += 2
				goto DISPATCH
			}
			if lutStringStop[s[i+3]] {
				i += 3
				goto DISPATCH
			}
			if lutStringStop[s[i+4]] {
				i += 4
				goto DISPATCH
			}
			if lutStringStop[s[i+5]] {
				i += 5
				goto DISPATCH
			}
			if lutStringStop[s[i+6]] {
				i += 6
				goto DISPATCH
			}
			if lutStringStop[s[i+7]] {
				i += 7
				goto DISPATCH
			}
			i += 8
		}
		for ; i < len(s); i++ {
			if lutStringStop[s[i]] {
				goto DISPATCH
			}
		}
		return i, esc, len(s), ErrUnexpectedEOF

	DISPATCH:
		switch s[i] {
		case '"':
			return i + 1, esc, 0, nil

		case '\\':
			// EscapedCharacter
			// (https://spec.graphql.org/September2025/#EscapedCharacter).
			esc = true
			if i+1 >= len(s) {
				return i, esc, i, ErrUnexpectedEOF
			}
			switch s[i+1] {
			case '"', '\\', '/', 'b', 'f', 'n', 'r', 't':
				i += 2
			case 'u':
				// EscapedUnicode
				// (https://spec.graphql.org/September2025/#EscapedUnicode).
				if i+2 >= len(s) {
					return i, esc, i + 1, ErrUnexpectedEOF
				}
				if s[i+2] == '{' {
					// Variable-width form `\u{HexDigit+}` (spec of September 2025).
					j := i + 3
					// Leading zeros are permitted. Stop accumulating once out of
					// range: v must not overflow.
					var v uint32
					outOfRange := false
					for ; j < len(s) && lutHex[s[j]]; j++ {
						if !outOfRange {
							v = v<<4 | hexByteValue(s[j])
							outOfRange = v > 0x10FFFF
						}
					}
					if j >= len(s) {
						return i, esc, i + 1, ErrUnexpectedEOF
					}
					if j == i+3 || s[j] != '}' {
						// No hex digits, or an unexpected non-hex character.
						return i, esc, i + 1, ErrInvalidEscape
					}
					if outOfRange || !isUnicodeScalarValue(v) {
						// Above U+10FFFF, or a surrogate code point.
						return i, esc, i + 1, ErrInvalidEscape
					}
					i = j + 1
					continue
				}
				// Fixed-width form `\uXXXX`.
				if i+5 >= len(s) {
					return i, esc, i + 1, ErrUnexpectedEOF
				}
				if !lutHex[s[i+2]] || !lutHex[s[i+3]] ||
					!lutHex[s[i+4]] || !lutHex[s[i+5]] {
					return i, esc, i + 1, ErrInvalidEscape
				}
				leading := fixedWidthEscapeValue(s[i+2:])
				if isLeadingSurrogate(leading) {
					// A Leading Surrogate is only legal as the first half of a
					// surrogate pair. The second half is another fixed-width
					// escape holding a Trailing Surrogate.
					if i+6 >= len(s) {
						return i, esc, i + 1, ErrUnexpectedEOF
					}
					if s[i+6] != '\\' {
						return i, esc, i + 1, ErrInvalidEscape
					}
					if i+7 >= len(s) {
						return i, esc, i + 1, ErrUnexpectedEOF
					}
					if s[i+7] != 'u' {
						return i, esc, i + 1, ErrInvalidEscape
					}
					if i+11 >= len(s) {
						return i, esc, i + 1, ErrUnexpectedEOF
					}
					if !lutHex[s[i+8]] || !lutHex[s[i+9]] ||
						!lutHex[s[i+10]] || !lutHex[s[i+11]] {
						return i, esc, i + 1, ErrInvalidEscape
					}
					if !isTrailingSurrogate(fixedWidthEscapeValue(s[i+8:])) {
						return i, esc, i + 1, ErrInvalidEscape
					}
					i += 12
					continue
				}
				if !isUnicodeScalarValue(leading) {
					// A Trailing Surrogate without a preceding Leading Surrogate.
					return i, esc, i + 1, ErrInvalidEscape
				}
				i += 5
			default:
				return i, esc, i, ErrInvalidEscape
			}

		default:
			if s[i] < 0x20 {
				// A control character needs an escape sequence here.
				return i, esc, i, ErrUnescapedControlChar
			}
			// A multi-byte SourceCharacter, or no SourceCharacter at all.
			n := sourceCharacterLen(s, i)
			if n == 0 {
				return i, esc, i, ErrMalformedUTF8
			}
			i += n
		}
	}
}

// scanStringBlock scans the contents of a block StringValue. i must be the index
// right after the opening `"""`. It returns the index right after the closing
// `"""`, the common indentation to strip from every line but the first, and
// whether the block holds anything but WhiteSpace and LineTerminators.
// Reference:
//
//   - https://spec.graphql.org/September2025/#sec-String-Value
func scanStringBlock(s string, i int) (
	end, prefixLen int, hasContent bool, errPos int, err error,
) {
	prefixLenSet := false
	// content is the index of the first byte that is neither WhiteSpace nor a
	// LineTerminator, or -1 while no such byte was seen.
	content := -1

	for i < len(s) {
		if !lutBlockStop[s[i]] {
			// A run of BlockStringCharacters that need no further inspection.
			if content < 0 {
				content = i
			}
			for i += 1; i+8 <= len(s); i += 8 {
				if !lutBlockStop[s[i]] &&
					!lutBlockStop[s[i+1]] &&
					!lutBlockStop[s[i+2]] &&
					!lutBlockStop[s[i+3]] &&
					!lutBlockStop[s[i+4]] &&
					!lutBlockStop[s[i+5]] &&
					!lutBlockStop[s[i+6]] &&
					!lutBlockStop[s[i+7]] {
					continue
				}
				break
			}
			for i < len(s) && !lutBlockStop[s[i]] {
				i++
			}
			continue
		}

		switch s[i] {
		case '"':
			if i+2 < len(s) && s[i+1] == '"' && s[i+2] == '"' {
				// End of the block string. A block that holds nothing but
				// WhiteSpace and LineTerminators has no content at all.
				return i + 3, prefixLen, content >= 0 && content != i, 0, nil
			}
			// Just a quote, not the end of the block string yet.
			if content < 0 {
				content = i
			}
		case '\\':
			if content < 0 {
				content = i
			}
			if hasPrefixAt(s, i, `\"""`) {
				// Escaped `\"""`.
				i += 4
				continue
			}
		case '\n', '\r':
			// Consume the LineTerminator, then the WhiteSpace that indents the
			// next line (https://spec.graphql.org/September2025/#WhiteSpace).
			c := 0
			for i += lineTerminatorLen(s, i); i < len(s) &&
				isWhiteSpace(s[i]); i, c = i+1, c+1 {
			}

			if i < len(s) && lineTerminatorLen(s, i) == 0 && content < 0 {
				content = i
			}

			// Only lines with non-whitespace text set the common indentation.
			// Blank lines and the closing-quote line are excluded
			// (https://spec.graphql.org/September2025/#BlockStringValue()).
			isBlankLine := i >= len(s) || lineTerminatorLen(s, i) > 0
			isLastLine := i+2 < len(s) && s[i] == '"' && s[i+1] == '"' && s[i+2] == '"'
			if !isBlankLine && !isLastLine {
				if prefixLenSet {
					prefixLen = min(prefixLen, c)
				} else {
					prefixLen, prefixLenSet = c, true
				}
			}
			continue
		case ' ', '\t':
			// Ignore WhiteSpace.
		default:
			// A BlockStringCharacter is any SourceCharacter, control ones
			// included.
			n := sourceCharacterLen(s, i)
			if n == 0 {
				return i, prefixLen, false, i, ErrMalformedUTF8
			}
			if content < 0 {
				content = i
			}
			i += n
			continue
		}
		i++
	}
	return i, prefixLen, false, len(s), ErrUnexpectedEOF
}

// The classes of [lutIgnorable].
const (
	// ignorableNone is a byte that is no Ignored token and hence ends the skip.
	ignorableNone = 0

	// ignorableByte is an Ignored token one byte wide: WhiteSpace, a
	// LineTerminator or the insignificant comma.
	ignorableByte = 1

	// ignorableMulti begins an Ignored token wider than one byte: a Comment or
	// a UnicodeBOM.
	ignorableMulti = 2
)

// lutIgnorable classifies a byte by the Ignored token it can begin.
// Reference:
//
//   - https://spec.graphql.org/September2025/#sec-Language.Source-Text.Ignored-Tokens
var lutIgnorable = [256]byte{
	' ': ignorableByte, ',': ignorableByte,
	'\t': ignorableByte, '\n': ignorableByte, '\r': ignorableByte,

	'#':          ignorableMulti,
	bomFirstByte: ignorableMulti,
}

// lutCommentStop marks the bytes that end the fast scan over a comment: the
// LineTerminators, which end the comment, and the non-ASCII bytes, which need
// to be checked for being a SourceCharacter.
var lutCommentStop = func() (t [256]bool) {
	t['\n'], t['\r'] = true, true
	for b := utf8.RuneSelf; b < len(t); b++ {
		t[b] = true
	}
	return t
}()

// lutStringStop marks the bytes that end the fast scan over a single-line
// string: the closing quote, the backslash of an escape sequence, the control
// characters, which are rejected, and the non-ASCII bytes, which need to be
// checked for being a SourceCharacter.
var lutStringStop = func() (t [256]bool) {
	t['"'], t['\\'] = true, true
	for b := range 0x20 {
		t[b] = true
	}
	for b := utf8.RuneSelf; b < len(t); b++ {
		t[b] = true
	}
	return t
}()

// lutBlockStop marks the bytes that end the fast scan over a block string:
// the quote of a delimiter, the backslash of a `\"""`, the LineTerminators and
// WhiteSpace, which the indentation is measured by, and the non-ASCII bytes,
// which need to be checked for being a SourceCharacter.
var lutBlockStop = func() (t [256]bool) {
	t['"'], t['\\'] = true, true
	t['\n'], t['\r'], t[' '], t['\t'] = true, true, true, true
	for b := utf8.RuneSelf; b < len(t); b++ {
		t[b] = true
	}
	return t
}()

// lutNameStart marks the bytes that begin a Name.
// Reference:
//
//   - https://spec.graphql.org/September2025/#NameStart
var lutNameStart = func() (t [256]bool) {
	for b := 'a'; b <= 'z'; b++ {
		t[b] = true
	}
	for b := 'A'; b <= 'Z'; b++ {
		t[b] = true
	}
	t['_'] = true
	return t
}()

// lutNameCont marks the bytes that continue a Name.
// Reference:
//
//   - https://spec.graphql.org/September2025/#NameContinue
var lutNameCont = func() (t [256]bool) {
	t = lutNameStart
	for b := '0'; b <= '9'; b++ {
		t[b] = true
	}
	return t
}()

// lutDigit marks the bytes representing a Digit.
// Reference:
//
//   - https://spec.graphql.org/September2025/#Digit
var lutDigit = func() (t [256]bool) {
	for b := '0'; b <= '9'; b++ {
		t[b] = true
	}
	return t
}()

// lutHex marks the hexadecimal digits.
// Reference:
//
//   - https://spec.graphql.org/September2025/#EscapedUnicode
var lutHex = func() (t [256]bool) {
	t = lutDigit
	for b := 'a'; b <= 'f'; b++ {
		t[b] = true
	}
	for b := 'A'; b <= 'F'; b++ {
		t[b] = true
	}
	return t
}()

// lutStringEscape marks the bytes of a string value that must be escaped: the
// backslash, which the escaping uses, and the control bytes the hash prefixes
// are taken from. Tab, line feed and carriage return are no prefixes and stay.
var lutStringEscape = func() (t [256]bool) {
	for b := range 0x20 {
		t[b] = true
	}
	t['\t'], t['\n'], t['\r'] = false, false, false
	t['\\'] = true
	return t
}()

// lutStringEscapeSeq holds the escape sequence of every byte.
// Adding 0x40 turns a control byte into a printable character.
var lutStringEscapeSeq = func() (t [256][2]byte) {
	for i := range t {
		t[i] = [2]byte{'\\', byte(i) + 0x40}
	}
	t['\\'] = [2]byte{'\\', '\\'}
	return t
}()
