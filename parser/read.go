package parser

// noopHash discards all writes. It is used to parse-but-not-hash sections that
// [Options] marks as ignored (e.g. variable definitions under [Options.IgnoreVariables]).
type noopHash struct{}

func (noopHash) Reset()                      {}
func (noopHash) Write(b []byte) (int, error) { return len(b), nil }
func (noopHash) Sum(b []byte) []byte         { return b }

func readDocument(h Hash, o Options, s []byte) (err error) {
	// [Options.IgnoreVariables] is a superset of [Options.IgnoreInputs]:
	// ignoring variables also ignores all input values.
	if o.IgnoreVariables {
		o.IgnoreInputs = true
	}
	s = SkipIgnorables(s)
	if err = ExpectNoEOF(s); err != nil {
		return err
	}
	for {
		if len(s) < 1 {
			return nil
		}
		if s, err = readDefinition(h, o, s); err != nil {
			return err
		}
		s = SkipIgnorables(s)
	}
}

func readDefinition(h Hash, o Options, s []byte) (suffix []byte, err error) {
	if err = ExpectNoEOF(s); err != nil {
		return s, err
	}
	switch {
	case s[0] == '{':
		// Anonymous operation.
		// (https://spec.graphql.org/September2025/#sec-Anonymous-Operation-Definitions)
		_, _ = h.Write(HPrefQuery)
		return readSelectionSet(h, o, s)

	case HasPrefix(s, "fragment"):
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
		if suffix, err = ReadToken(suffix, "on"); err != nil {
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
		if _, suffix, err = readDirectives(h, o, suffix); err != nil {
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
	if _, s, err = readDirectives(h, o, s); err != nil {
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

			if len(s) > len("on ") && HasPrefix(s, "on") && IsIgnorableByte(s[2]) {
				// Inline fragment
				// (https://spec.graphql.org/September2025/#InlineFragment).

				// Type condition
				// (https://spec.graphql.org/September2025/#TypeCondition).
				s = SkipIgnorables(s[3:])
				var typeName []byte
				if typeName, s, err = ReadName(s); err != nil {
					return s, err
				}
				_, _ = h.Write(HPrefInlineFragment)
				_, _ = h.Write(HPrefType)
				_, _ = h.Write([]byte(typeName))
				s = SkipIgnorables(s)

				// Optional directives.
				if _, s, err = readDirectives(h, o, s); err != nil {
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
				if _, s, err = readDirectives(h, o, s); err != nil {
					return s, err
				}
				s = SkipIgnorables(s)
			} else {
				// Inline fragment without type condition.
				_, _ = h.Write(HPrefInlineFragment)
				if _, s, err = readDirectives(h, o, s); err != nil {
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
				if _, s, err = readArguments(h, o, s); err != nil {
					return s, err
				}
				s = SkipIgnorables(s)
			}

			// Optional directives.
			if _, s, err = readDirectives(h, o, s); err != nil {
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

func readVariableDefinitionsAfterParenthesis(
	h Hash, o Options, s []byte) (suffix []byte, err error,
) {
	if o.IgnoreVariables {
		// Parse the definitions to advance the cursor, but hash nothing.
		h = noopHash{}
	}
	for {
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
		var typeDec []byte
		if typeDec, _, _, s, err = ReadType(s); err != nil {
			return s, err
		}
		s = SkipIgnorables(s)
		_, _ = h.Write(HPrefType)
		_, _ = h.Write([]byte(typeDec))

		// Optional default value.
		if err = ExpectNoEOF(s); err != nil {
			return s, err
		}
		if s[0] == '=' {
			s = s[1:]
			s = SkipIgnorables(s)
			if _, _, s, err = readValue(h, o, s); err != nil {
				return s, err
			}
			s = SkipIgnorables(s)
		}

		// Optional directives.
		if _, s, err = readDirectives(h, o, s); err != nil {
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

func readDirectives(h Hash, o Options, s []byte) (directives, suffix []byte, err error) {
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
			if _, suffix, err = readArguments(h, o, suffix); err != nil {
				return directives, suffix, err
			}
		}
		suffix = SkipIgnorables(suffix)
	}
	return s[:len(s)-len(suffix)], suffix, nil
}

func readArguments(h Hash, o Options, s []byte) (arguments, suffix []byte, err error) {
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
		if _, _, suffix, err = readValue(h, o, suffix); err != nil {
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
	if !HasPrefix(s, kw) {
		return false
	}
	n := len(kw)
	return n == len(s) || (!IsLetter(s[n]) && !IsDigit(s[n]) && s[n] != '_')
}

func readValue(h Hash, o Options, s []byte) (
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
		if value, suffix, err = ReadIntValue(s); err != nil {
			return value, ValueTypeInt, suffix, err
		}
		if len(suffix) > 0 && (suffix[0] == '.' || suffix[0] == 'e' || suffix[0] == 'E') {
			if _, suffix, err = ReadFloatAfterInteger(suffix); err != nil {
				return value, ValueTypeFloat, suffix, err
			}
			// FloatValue (https://spec.graphql.org/September2025/#sec-Float-Value).
			value = s[:len(s)-len(suffix)]
			_, _ = h.Write(HPrefValueFloat)
			_, _ = h.Write([]byte(value))
			return value, ValueTypeFloat, suffix, nil
		}
		// IntValue (https://spec.graphql.org/September2025/#sec-Int-Value).
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

			_, _ = h.Write(HPrefValueString)
			for line := range IterateBlockStringLines(value, prefixLen) {
				_, _ = h.Write(line)
			}

			return value, ValueTypeStringBlock, suffix, nil
		} else { // String.
			if _, suffix, err = ReadStringLineAfterQuotes(s[1:]); err != nil {
				return value, ValueTypeString, suffix, err
			}
			value = s[1 : len(s)-len(suffix)-1]
			_, _ = h.Write(HPrefValueString)
			_, _ = h.Write([]byte(value))
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
			if _, _, suffix, err = readValue(h, o, suffix); err != nil {
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
			if _, _, suffix, err = readValue(h, o, suffix); err != nil {
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
