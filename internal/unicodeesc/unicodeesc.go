// Package unicodeesc reads the \uXXXX escape that JSON and GraphQL share.
//
// Both spell a code point as four hexadecimal digits, and both spell one above
// 0xFFFF as a pair of surrogates. What a half of a pair on its own means is
// where the two part ways: JSON replaces it, GraphQL rejects it.
// So this package answers what a value is and leaves what to do about it to the caller.
package unicodeesc

// Source is a document, held either way a caller of this module holds one.
type Source interface{ ~string | ~[]byte }

// lutHex marks the hexadecimal digits.
var lutHex = func() (t [256]bool) {
	for _, b := range []byte("0123456789abcdefABCDEF") {
		t[b] = true
	}
	return t
}()

// IsHexDigit reports whether b is a hexadecimal digit.
func IsHexDigit(b byte) bool { return lutHex[b] }

// DigitValue returns the value of the hexadecimal digit b.
// The result is meaningless unless [IsHexDigit] reports true for b.
func DigitValue(b byte) uint32 {
	switch {
	case b >= '0' && b <= '9':
		return uint32(b - '0')
	case b >= 'a' && b <= 'f':
		return uint32(b-'a') + 10
	}
	return uint32(b-'A') + 10
}

// Value returns the value of the four hexadecimal digits s begins with.
// The result is meaningless unless all four are digits, which a caller that
// hasn't checked them already gets from [Hex4] instead.
//
// s must hold at least four bytes.
func Value[S Source](s S) uint32 {
	return DigitValue(s[0])<<12 | DigitValue(s[1])<<8 |
		DigitValue(s[2])<<4 | DigitValue(s[3])
}

// Hex4 returns the value of the four hexadecimal digits s begins with,
// and whether all four are digits.
//
// s must hold at least four bytes.
func Hex4[S Source](s S) (uint32, bool) {
	if !IsHexDigit(s[0]) || !IsHexDigit(s[1]) ||
		!IsHexDigit(s[2]) || !IsHexDigit(s[3]) {
		return 0, false
	}
	return Value(s), true
}

// IsScalarValue reports whether v is a Unicode scalar value. The surrogate code
// points 0xD800-0xDFFF are not, being halves of a pair rather than characters.
// Reference:
//
//   - https://spec.graphql.org/September2025/#sec-Unicode
func IsScalarValue(v uint32) bool {
	return v <= 0xD7FF || (v >= 0xE000 && v <= 0x10FFFF)
}

// IsLeadingSurrogate reports whether v is the first half of a surrogate pair.
// Reference:
//
//   - https://spec.graphql.org/September2025/#StringCharacter
func IsLeadingSurrogate(v uint32) bool { return v >= 0xD800 && v <= 0xDBFF }

// IsTrailingSurrogate reports whether v is the second half of a surrogate pair.
// Reference:
//
//   - https://spec.graphql.org/September2025/#StringCharacter
func IsTrailingSurrogate(v uint32) bool { return v >= 0xDC00 && v <= 0xDFFF }

// Pair returns the code point a surrogate pair spells out.
// The result is meaningless unless leading and trailing are the two halves,
// which [IsLeadingSurrogate] and [IsTrailingSurrogate] report.
func Pair(leading, trailing uint32) uint32 {
	return 0x10000 + (leading-0xD800)<<10 + (trailing - 0xDC00)
}
