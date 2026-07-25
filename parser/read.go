package parser

import (
	"bytes"
	"unicode/utf8"
)

// noopHash discards all writes. It is used to parse-but-not-hash sections that
// [Options] marks as ignored (e.g. variable definitions under [Options.IgnoreVariables]).
type noopHash struct{}

func (noopHash) Reset()                      {}
func (noopHash) Write(b []byte) (int, error) { return len(b), nil }
func (noopHash) Sum(b []byte) []byte         { return b }

func readDocument(h Hash, o Options, input []byte) Error {
	// [Options.IgnoreVariables] is a superset of [Options.IgnoreInputs]:
	// ignoring variables also ignores all input values.
	if o.IgnoreVariables {
		o.IgnoreInputs = true
	}
	s := SkipIgnorables(input)
	if err := ExpectNoEOF(s); err != nil {
		return newError(input, s, err)
	}
	for {
		if len(s) < 1 {
			return Error{}
		}
		var err error
		if s, err = readDefinition(h, o, s); err != nil {
			return newError(input, s, err)
		}
		s = SkipIgnorables(s)
	}
}

func readDefinition(h Hash, o Options, s []byte) (suffix []byte, err error) {
	if err = ExpectNoEOF(s); err != nil {
		return s, err
	}

	var described bool
	if described, s, err = readDescription(o, s); err != nil {
		return s, err
	}
	if err = ExpectNoEOF(s); err != nil {
		return s, err
	}

	switch {
	case s[0] == '{':
		// Anonymous operation.
		// (https://spec.graphql.org/September2025/#sec-Anonymous-Operation-Definitions)
		if described {
			// Query shorthand is no OperationDefinition with an OperationType,
			// which is the only form that takes a description.
			return s, ErrUnexpectedToken
		}
		_, _ = h.Write(HPrefQuery)
		return readSelectionSet(h, o, s)

	case isKeyword(s, "fragment"):
		// FragmentDefinition
		// (https://spec.graphql.org/September2025/#FragmentDefinition).
		s = s[len("fragment"):]
		s = SkipIgnorables(s)

		// FragmentName
		// (https://spec.graphql.org/September2025/#FragmentName).
		var name []byte
		if name, suffix, err = ReadName(s); err != nil {
			return suffix, err
		}
		if string(name) == "on" {
			return s, ErrUnexpectedToken // Return suffix as []byte.
		}

		// TypeCondition
		// (https://spec.graphql.org/September2025/#TypeCondition).
		suffix = SkipIgnorables(suffix)
		if suffix, err = readKeyword(suffix, "on"); err != nil {
			return suffix, err
		}
		suffix = SkipIgnorables(suffix)
		var typeDec []byte
		if typeDec, suffix, err = ReadName(suffix); err != nil {
			return suffix, err
		}
		suffix = SkipIgnorables(suffix)
		_, _ = h.Write(HPrefFragmentDefinition)
		_, _ = h.Write([]byte(name))
		_, _ = h.Write(HPrefType)
		_, _ = h.Write([]byte(typeDec))

		// Optional directives.
		if _, suffix, err = readDirectives(h, o, suffix, false); err != nil {
			return suffix, err
		}
		suffix = SkipIgnorables(suffix)

		return readSelectionSet(h, o, suffix)
	}

	return readOperationDefinition(h, o, s)
}

func readOperationDefinition(h Hash, o Options, s []byte) (suffix []byte, err error) {
	if _, s, err = ReadOperationType(h, s); err != nil {
		return s, err
	}
	s = SkipIgnorables(s)
	if err = ExpectNoEOF(s); err != nil {
		return s, err
	}

	// Optional name.
	if IsNameStart(s[0]) {
		// [ReadName] can't fail here: s is non-empty and begins with a NameStart.
		var name []byte
		name, s, _ = ReadName(s)
		_, _ = h.Write([]byte(name))

		s = SkipIgnorables(s)
		if err = ExpectNoEOF(s); err != nil {
			return s, err
		}
	}

	// Optional variable definitions.
	if s[0] == '(' {
		s = SkipIgnorables(s[1:])
		if err = ExpectNoEOF(s); err != nil {
			return s, err
		}
		if s, err = readVariableDefinitionsAfterParenthesis(h, o, s); err != nil {
			return s, err
		}
		s = SkipIgnorables(s)
	}

	// Optional directives.
	if _, s, err = readDirectives(h, o, s, false); err != nil {
		return s, err
	}
	s = SkipIgnorables(s)

	return readSelectionSet(h, o, s)
}

func readSelectionSet(h Hash, o Options, s []byte) (suffix []byte, err error) {
	if s, err = ReadToken(s, "{"); err != nil {
		return s, err
	}
	s = SkipIgnorables(s)

	_, _ = h.Write(HPrefSelectionSet)

	for {
		if HasPrefix(s, "...") {
			// Fragment spread or inline fragment
			// (https://spec.graphql.org/September2025/#Selection).
			s = s[len("..."):]
			s = SkipIgnorables(s)

			if isKeyword(s, "on") {
				// Inline fragment
				// (https://spec.graphql.org/September2025/#InlineFragment).
				// A FragmentSpread can't be named "on", so a bare "on" keyword
				// always begins a type condition.

				// Type condition
				// (https://spec.graphql.org/September2025/#TypeCondition).
				s = SkipIgnorables(s[len("on"):])
				var typeName []byte
				if typeName, s, err = ReadName(s); err != nil {
					return s, err
				}
				_, _ = h.Write(HPrefInlineFragment)
				_, _ = h.Write(HPrefType)
				_, _ = h.Write([]byte(typeName))
				s = SkipIgnorables(s)

				// Optional directives.
				if _, s, err = readDirectives(h, o, s, false); err != nil {
					return s, err
				}

				s = SkipIgnorables(s)
				if s, err = readSelectionSet(h, o, s); err != nil { // Recurse.
					return s, err
				}
				s = SkipIgnorables(s)

			} else if len(s) > 0 && IsNameStart(s[0]) {
				// Fragment spread
				// (https://spec.graphql.org/September2025/#FragmentSpread).

				// Fragment name
				// (https://spec.graphql.org/September2025/#FragmentName).
				// [ReadName] can't fail here: s is non-empty and begins with a NameStart.
				var fragName []byte
				fragName, s, _ = ReadName(s)
				_, _ = h.Write(HPrefFragmentSpread)
				_, _ = h.Write([]byte(fragName))
				s = SkipIgnorables(s)

				// Optional directives.
				if _, s, err = readDirectives(h, o, s, false); err != nil {
					return s, err
				}
				s = SkipIgnorables(s)
			} else {
				// Inline fragment without type condition.
				_, _ = h.Write(HPrefInlineFragment)
				if _, s, err = readDirectives(h, o, s, false); err != nil {
					return s, err
				}
				s = SkipIgnorables(s)
				if s, err = readSelectionSet(h, o, s); err != nil { // Recurse.
					return s, err
				}
				s = SkipIgnorables(s)
			}
		} else {
			// Field (https://spec.graphql.org/September2025/#Field).
			var name []byte
			if name, s, err = ReadName(s); err != nil { // Name or alias.
				return s, err
			}
			_, _ = h.Write(HPrefField)
			_, _ = h.Write([]byte(name))

			s = SkipIgnorables(s)
			if err = ExpectNoEOF(s); err != nil {
				return s, err
			}
			if s[0] == ':' {
				// The name above was an alias.
				s = SkipIgnorables(s[1:])
				var aliased []byte
				if aliased, s, err = ReadName(s); err != nil { // Actual field name.
					return s, err
				}
				_, _ = h.Write(HPrefFieldAliasedName)
				_, _ = h.Write([]byte(aliased))
				s = SkipIgnorables(s)
			}

			// Optional arguments.
			if err = ExpectNoEOF(s); err != nil {
				return s, err
			}
			if s[0] == '(' {
				if _, s, err = readArguments(h, o, s, false); err != nil {
					return s, err
				}
				s = SkipIgnorables(s)
			}

			// Optional directives.
			if _, s, err = readDirectives(h, o, s, false); err != nil {
				return s, err
			}
			s = SkipIgnorables(s)

			// Optional selection set.
			if err = ExpectNoEOF(s); err != nil {
				return s, err
			}
			if s[0] == '{' {
				if s, err = readSelectionSet(h, o, s); err != nil { // Recurse.
					return s, err
				}
			}
			s = SkipIgnorables(s)
		}
		if err = ExpectNoEOF(s); err != nil {
			return s, err
		}
		if s[0] == '}' { // End of selection set.
			s = s[1:]
			_, _ = h.Write(HPrefSelectionSetEnd)
			break
		}
	}
	return s, nil
}

// readDescription reads the optional Description of a definition and skips the
// Ignored tokens after it. A description is documentation, it must not affect
// execution, so it's parsed but never hashed.
// Reference:
//
//   - https://spec.graphql.org/September2025/#sec-Descriptions
func readDescription(o Options, s []byte) (found bool, suffix []byte, err error) {
	if len(s) < 1 || s[0] != '"' {
		return false, s, nil
	}
	if _, _, suffix, err = readValue(noopHash{}, o, s, true); err != nil {
		return true, suffix, err
	}
	return true, SkipIgnorables(suffix), nil
}

func readVariableDefinitionsAfterParenthesis(
	h Hash, o Options, s []byte) (suffix []byte, err error,
) {
	if o.IgnoreVariables {
		// Parse the definitions to advance the cursor, but hash nothing.
		h = noopHash{}
	}
	for {
		if _, s, err = readDescription(o, s); err != nil {
			return s, err
		}
		if err = ExpectNoEOF(s); err != nil {
			return s, err
		}
		if s[0] != '$' {
			return s, ErrUnexpectedToken
		}
		s = SkipIgnorables(s[1:])
		var name []byte
		if name, s, err = ReadName(s); err != nil {
			return s, err
		}
		_, _ = h.Write(HPrefVariableDefinition)
		_, _ = h.Write([]byte(name))

		s = SkipIgnorables(s)
		if s, err = ReadToken(s, ":"); err != nil {
			return s, err
		}

		// Type.
		s = SkipIgnorables(s)
		_, _ = h.Write(HPrefType)
		if _, _, _, s, err = readType(h, s); err != nil {
			return s, err
		}
		s = SkipIgnorables(s)

		// Optional default value. It and the directives below take
		// Value[Const], which excludes variable usages
		// (https://spec.graphql.org/September2025/#VariableDefinition).
		if err = ExpectNoEOF(s); err != nil {
			return s, err
		}
		if s[0] == '=' {
			s = s[1:]
			s = SkipIgnorables(s)
			if _, _, s, err = readValue(h, o, s, true); err != nil {
				return s, err
			}
			s = SkipIgnorables(s)
		}

		// Optional directives.
		if _, s, err = readDirectives(h, o, s, true); err != nil {
			return s, err
		}
		s = SkipIgnorables(s)

		if err = ExpectNoEOF(s); err != nil {
			return s, err
		}
		if s[0] == ')' { // End variable definitions.
			s = s[1:]
			break
		}
	}

	return s, err
}

// readType reads Type and writes its canonical form to h - name, brackets and
// '!' - without the Ignored tokens between them, so formatting within a type
// reference doesn't change the hash.
// Reference:
//
//   - https://spec.graphql.org/September2025/#Type
func readType(h Hash, s []byte) (
	typeDef []byte, nullable, array bool, suffix []byte, err error,
) {
	suffix, nullable = s, true
	if err = ExpectNoEOF(suffix); err != nil {
		return typeDef, nullable, array, suffix, err
	}
	switch {
	case IsNameStart(suffix[0]):
		// [ReadName] can't fail here: suffix is non-empty and begins with a
		// NameStart, which are its only two error conditions.
		var name []byte
		name, suffix, _ = ReadName(suffix)
		_, _ = h.Write(name)
	case suffix[0] == '[':
		array = true
		_, _ = h.Write(hBracketLeft)
		suffix = SkipIgnorables(suffix[1:])
		// Recurse.
		if _, _, _, suffix, err = readType(h, suffix); err != nil {
			return typeDef, nullable, array, suffix, err
		}
		suffix = SkipIgnorables(suffix)
		if err = ExpectNoEOF(suffix); err != nil {
			return typeDef, nullable, array, suffix, err
		}
		if suffix[0] != ']' {
			return typeDef, nullable, array, suffix, ErrUnexpectedToken
		}
		_, _ = h.Write(hBracketRight)
		suffix = suffix[1:]
	default:
		return typeDef, nullable, array, suffix, ErrUnexpectedToken
	}
	{
		s := SkipIgnorables(suffix)
		if len(s) > 0 && s[0] == '!' {
			nullable, suffix = false, s[1:]
			_, _ = h.Write(hExclamation)
		}
	}
	return s[:len(s)-len(suffix)], nullable, array, suffix, err
}

func readDirectives(
	h Hash, o Options, s []byte, constant bool,
) (directives, suffix []byte, err error) {
	suffix = s
	for len(suffix) > 0 {
		if suffix[0] != '@' {
			break
		}
		suffix = SkipIgnorables(suffix[1:])
		var name []byte
		if name, suffix, err = ReadName(suffix); err != nil {
			return directives, suffix, err
		}
		_, _ = h.Write(HPrefDirective)
		_, _ = h.Write([]byte(name))

		suffix = SkipIgnorables(suffix)
		if err = ExpectNoEOF(suffix); err != nil {
			return directives, suffix, err
		}
		if suffix[0] == '(' {
			if _, suffix, err = readArguments(h, o, suffix, constant); err != nil {
				return directives, suffix, err
			}
		}
		suffix = SkipIgnorables(suffix)
	}
	return s[:len(s)-len(suffix)], suffix, nil
}

func readArguments(
	h Hash, o Options, s []byte, constant bool,
) (arguments, suffix []byte, err error) {
	if suffix, err = ReadToken(s, "("); err != nil {
		return arguments, suffix, err
	}
	suffix = s[1:]
	suffix = SkipIgnorables(suffix)
	for {
		var name []byte
		if name, suffix, err = ReadName(suffix); err != nil {
			return arguments, suffix, err
		}
		_, _ = h.Write(HPrefArgument)
		_, _ = h.Write([]byte(name))

		suffix = SkipIgnorables(suffix)
		if suffix, err = ReadToken(suffix, ":"); err != nil {
			return arguments, suffix, err
		}

		suffix = SkipIgnorables(suffix)
		if _, _, suffix, err = readValue(h, o, suffix, constant); err != nil {
			return arguments, suffix, err
		}

		suffix = SkipIgnorables(suffix)
		if err = ExpectNoEOF(suffix); err != nil {
			return arguments, suffix, err
		}
		if suffix[0] == ')' { // End of arguments.
			suffix = suffix[1:]
			break
		}
	}
	return s[:len(s)-len(suffix)], suffix, nil
}

// isKeyword returns true if s begins with the whole word kw and not just a
// longer name starting with it, so the enum "nullable" won't match "null".
func isKeyword(s []byte, kw string) bool {
	return HasPrefix(s, kw) && (len(kw) == len(s) || !IsNameContinue(s[len(kw)]))
}

// readKeyword reads the keyword kw. Unlike [ReadToken], which reads punctuators,
// kw must be a whole Name and not merely the start of a longer one, because
// Name doesn't allow a NameContinue to follow it.
// Reference:
//
//   - https://spec.graphql.org/September2025/#Name
func readKeyword(s []byte, kw string) (suffix []byte, err error) {
	if err = ExpectNoEOF(s); err != nil {
		return s, err
	}
	if !isKeyword(s, kw) {
		return s, ErrUnexpectedToken
	}
	return s[len(kw):], nil
}

// writeStringValue writes a single-line string with its escape sequences
// evaluated, so two spellings of one value hash alike. s is the source between
// the quotes, already validated by [ReadStringLineAfterQuotes].
// Reference:
//
//   - https://spec.graphql.org/September2025/#sec-String-Value
func writeStringValue(h Hash, s []byte) {
	for {
		i := bytes.IndexByte(s, '\\')
		if i < 0 {
			// The rest stands for itself and needs no escaping: control bytes
			// are rejected and every backslash begins an escape sequence.
			_, _ = h.Write(s)
			return
		}
		_, _ = h.Write(s[:i])
		s = s[i:]

		switch s[1] {
		case 'b':
			writeStringByte(h, '\b')
			s = s[2:]
		case 'f':
			writeStringByte(h, '\f')
			s = s[2:]
		case 'n':
			writeStringByte(h, '\n')
			s = s[2:]
		case 'r':
			writeStringByte(h, '\r')
			s = s[2:]
		case 't':
			writeStringByte(h, '\t')
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
			writeStringRune(h, rune(v))
		default:
			// `\"`, `\\` and `\/` stand for the character itself.
			writeStringByte(h, s[1])
			s = s[2:]
		}
	}
}

// writeBlockStringLine writes one line of a block string. Its only escape is
// `\"""`, which stands for `"""`.
// Reference:
//
//   - https://spec.graphql.org/September2025/#BlockStringCharacter
func writeBlockStringLine(h Hash, s []byte) {
	const escapedTripleQuote = `\"""`
	start := 0
	for i := 0; i < len(s); i++ {
		if !mustEscapeInStringValue(s[i]) {
			continue
		}
		_, _ = h.Write(s[start:i])
		if HasPrefix(s[i:], escapedTripleQuote) {
			_, _ = h.Write(hTripleQuote)
			i += len(escapedTripleQuote) - 1 // The loop skips the last byte.
		} else {
			writeEscapedStringByte(h, s[i])
		}
		start = i + 1
	}
	_, _ = h.Write(s[start:])
}

func writeStringByte(h Hash, b byte) {
	if mustEscapeInStringValue(b) {
		writeEscapedStringByte(h, b)
		return
	}
	_, _ = h.Write(allBytes[b : b+1])
}

// writeStringRune writes r as UTF-8, byte by byte: a buffer handed to
// [Hash.Write] would escape to the heap.
func writeStringRune(h Hash, r rune) {
	var buf [utf8.UTFMax]byte
	n := utf8.EncodeRune(buf[:], r)
	for _, b := range buf[:n] {
		writeStringByte(h, b)
	}
}

// writeEscapedStringByte writes b as a backslash escape, which is what keeps a
// string value from containing a byte that looks like a hash prefix.
func writeEscapedStringByte(h Hash, b byte) {
	_, _ = h.Write(lutStringEscapeSeq[b][:])
}

// lutStringEscapeSeq keeps every escape sequence ready, so writing one doesn't
// allocate. Adding 0x40 turns a control byte into a printable character.
var lutStringEscapeSeq = func() (t [256][2]byte) {
	for i := range t {
		t[i] = [2]byte{'\\', byte(i) + 0x40}
	}
	t['\\'] = [2]byte{'\\', '\\'}
	return t
}()

func mustEscapeInStringValue(b byte) bool { return lutStringEscape[b] }

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

func fixedWidthEscapeValue(s []byte) uint32 {
	return hexByteValue(s[0])<<12 | hexByteValue(s[1])<<8 |
		hexByteValue(s[2])<<4 | hexByteValue(s[3])
}

func readValue(h Hash, o Options, s []byte, constant bool) (
	value []byte, valueType ValueType, suffix []byte, err error,
) {
	if err = ExpectNoEOF(s); err != nil {
		return value, valueType, s, err
	}
	// Under [Options.IgnoreInputs] all values are hashed to a discard hasher,
	// including the items of lists and input objects. The variable signature is
	// still kept by [readVariableDefinitionsAfterParenthesis] (unless
	// [Options.IgnoreVariables], which drops it too).
	if o.IgnoreInputs {
		h = noopHash{}
	}
	switch {
	case isKeyword(s, "null"):
		// NullValue (https://spec.graphql.org/September2025/#sec-Null-Value).
		_, _ = h.Write(HPrefValueNull)
		return s[:len("null")], ValueTypeNull, s[len("null"):], nil

	case isKeyword(s, "true"):
		// BooleanValue (https://spec.graphql.org/September2025/#sec-Boolean-Value).
		_, _ = h.Write(HPrefValueTrue)
		return s[:len("true")], ValueTypeBooleanTrue, s[len("true"):], nil

	case isKeyword(s, "false"):
		// BooleanValue (https://spec.graphql.org/September2025/#sec-Boolean-Value).
		_, _ = h.Write(HPrefValueFalse)
		return s[:len("false")], ValueTypeBooleanFalse, s[len("false"):], nil

	case s[0] == '$':
		// Variable (https://spec.graphql.org/September2025/#Variable).
		if constant {
			// Value[Const] has no Variable, not even nested in a list or an
			// input object, because the caller passes constant down.
			return value, ValueTypeVariable, s, ErrUnexpectedVariable
		}
		s = SkipIgnorables(s[1:])
		if _, suffix, err = ReadName(s); err != nil {
			return s, ValueTypeVariable, suffix, err
		}
		value = s[:len(s)-len(suffix)]
		// A variable usage is just another input value: written to h, which is
		// the discard hasher under [Options.IgnoreInputs].
		_, _ = h.Write(HPrefValueVariable)
		_, _ = h.Write([]byte(value))
		return value, ValueTypeVariable, suffix, err

	case s[0] == '-' || IsDigit(s[0]):
		// IntValue and FloatValue share the IntegerPart, so it's read before
		// the two can be told apart. Both must end on [expectNumberEnd].
		if value, suffix, err = readIntegerPart(s); err != nil {
			return value, ValueTypeInt, suffix, err
		}
		if len(suffix) > 0 && (suffix[0] == '.' || suffix[0] == 'e' || suffix[0] == 'E') {
			// FloatValue (https://spec.graphql.org/September2025/#sec-Float-Value).
			if _, suffix, err = ReadFloatAfterInteger(suffix); err != nil {
				return value, ValueTypeFloat, suffix, err
			}
			value = s[:len(s)-len(suffix)]
			_, _ = h.Write(HPrefValueFloat)
			_, _ = h.Write([]byte(value))
			return value, ValueTypeFloat, suffix, nil
		}
		// IntValue (https://spec.graphql.org/September2025/#sec-Int-Value).
		if err = expectNumberEnd(suffix); err != nil {
			return value, ValueTypeInt, suffix, err
		}
		_, _ = h.Write(HPrefValueInteger)
		_, _ = h.Write([]byte(value))
		return value, ValueTypeInt, suffix, nil

	case s[0] == '"': // String or block string.
		if HasPrefix(s, `"""`) { // Block string.
			var prefixLen int
			value, prefixLen, suffix, err = ReadStringBlockAfterQuotes(s[3:])
			if err != nil {
				return value, ValueTypeStringBlock, suffix, err
			}
			if value != nil {
				value = s[3 : len(s)-len(suffix)-3]
				value = TrimEmptyLinesSuffix(value)
			}

			// BlockStringValue joins the lines with a single line feed, which
			// is what normalizes LF, CRLF and CR to the same hash.
			_, _ = h.Write(HPrefValueString)
			firstLine := true
			for line := range IterateBlockStringLines(value, prefixLen) {
				if !firstLine {
					_, _ = h.Write(hLineFeed)
				}
				firstLine = false
				writeBlockStringLine(h, line)
			}

			return value, ValueTypeStringBlock, suffix, nil
		} else { // String.
			if _, suffix, err = ReadStringLineAfterQuotes(s[1:]); err != nil {
				return value, ValueTypeString, suffix, err
			}
			value = s[1 : len(s)-len(suffix)-1]
			_, _ = h.Write(HPrefValueString)
			writeStringValue(h, value)
			return value, ValueTypeString, suffix, nil
		}

	case s[0] == '[':
		// ListValue (https://spec.graphql.org/September2025/#sec-List-Value).
		_, _ = h.Write(HPrefValueList)
		suffix = SkipIgnorables(s[1:])
		if len(suffix) > 0 && suffix[0] == ']' {
			suffix = suffix[1:]
			return s[:len(s)-len(suffix)], ValueTypeList, suffix, nil
		}
		for len(suffix) > 0 {
			if _, _, suffix, err = readValue(h, o, suffix, constant); err != nil {
				return value, ValueTypeList, suffix, err
			}
			suffix = SkipIgnorables(suffix)
			if err = ExpectNoEOF(suffix); err != nil {
				return value, ValueTypeList, suffix, err
			}
			if suffix[0] == ']' { // End of list.
				_, _ = h.Write(HPrefValueListEnd)
				return s[:len(s)-len(suffix[1:])], ValueTypeList, suffix[1:], nil
			}
		}
		return value, ValueTypeList, suffix, ErrUnexpectedEOF

	case s[0] == '{':
		// InputObject (https://spec.graphql.org/September2025/#sec-Input-Object-Values).
		_, _ = h.Write(HPrefValueInputObject)
		suffix = SkipIgnorables(s[1:])
		if len(suffix) > 0 && suffix[0] == '}' {
			suffix = suffix[1:]
			return s[:len(s)-len(suffix)], ValueTypeInputObject, suffix, nil
		}
		for len(suffix) > 0 {
			// ObjectField (https://spec.graphql.org/September2025/#ObjectField).
			var name []byte
			if name, suffix, err = ReadName(suffix); err != nil {
				return value, ValueTypeInputObject, suffix, err
			}
			_, _ = h.Write(HPrefValueInputObjectField)
			_, _ = h.Write([]byte(name))

			// Column.
			suffix = SkipIgnorables(suffix)
			if err = ExpectNoEOF(suffix); err != nil {
				return value, ValueTypeInputObject, suffix, err
			}
			if suffix[0] != ':' {
				return value, ValueTypeInputObject, suffix, ErrUnexpectedToken
			}
			suffix = suffix[1:]

			// Value.
			suffix = SkipIgnorables(suffix)
			if _, _, suffix, err = readValue(h, o, suffix, constant); err != nil {
				return value, ValueTypeInputObject, suffix, err
			}
			suffix = SkipIgnorables(suffix)
			if err = ExpectNoEOF(suffix); err != nil {
				return value, ValueTypeInputObject, suffix, err
			}
			if suffix[0] == '}' { // End of input object.
				_, _ = h.Write(HPrefInputObjectEnd)
				return s[:len(s)-len(suffix[1:])], ValueTypeInputObject, suffix[1:], nil
			}
		}
		return value, ValueTypeInputObject, suffix, ErrUnexpectedEOF

	default:
		// EnumValue (https://spec.graphql.org/September2025/#sec-Enum-Value).
		value, suffix, err = ReadName(s)
		valueType = ValueTypeEnum
		if err != nil {
			valueType = 0
		}
		_, _ = h.Write(HPrefValueEnum)
		_, _ = h.Write([]byte(value))
		return value, valueType, suffix, err
	}
}
