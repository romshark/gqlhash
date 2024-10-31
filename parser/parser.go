// Package parser provides GraphQL query hashing functions for
// the latest GraphQL specification: https://spec.graphql.org/October2021/.
package parser

import (
	"errors"
	"hash"
)

var (
	ErrUnexpectedEOF   = errors.New("unexpected EOF")
	ErrUnexpectedToken = errors.New("unexpected token")
)

// Hash is a subset of the standard `hash.Hash`.
type Hash interface {
	Reset()
	Sum([]byte) []byte
	Write([]byte) (int, error)
}

var _ Hash = hash.Hash(nil)

// The hash prefixes are used as magic bytes before writing actual query contents to
// prevent tokens from collapsing into one if separators aren't written, for example:
// query fields `{ foo bar }` might collapse into one field `{ foobar }`
// producing the same hash for those two different queries.
var (
	HPrefQuery                 = []byte("/q")
	HPrefMutation              = []byte("/m")
	HPrefSubscription          = []byte("/s")
	HPrefFragmentDefinition    = []byte("/g")
	HPrefVariableDefinition    = []byte("/r")
	HPrefDirective             = []byte("/d")
	HPrefField                 = []byte("/f")
	HPrefType                  = []byte("/t")
	HPrefFieldAliasedName      = []byte("/u") // The actual name of the aliased field.
	HPrefFragmentSpread        = []byte("/p")
	HPrefInlineFragment        = []byte("/i")
	HPrefArgument              = []byte("/a")
	HPrefSelectionSet          = []byte("/c")
	HPrefSelectionSetEnd       = []byte("/e")
	HPrefValueInputObject      = []byte("/b")
	HPrefValueInputObjectField = []byte("/k")
	HPrefInputObjectEnd        = []byte("/z")
	HPrefValueNull             = []byte("/0")
	HPrefValueTrue             = []byte("/1")
	HPrefValueFalse            = []byte("/2")
	HPrefValueInteger          = []byte("/3")
	HPrefValueFloat            = []byte("/4")
	HPrefValueEnum             = []byte("/5")
	HPrefValueString           = []byte("/6")
	HPrefValueStringBlock      = []byte("/7")
	HPrefValueList             = []byte("/8")
	HPrefValueListEnd          = []byte("/9")
	HPrefValueVariable         = []byte("/v")
)

// ReadDocument reads one or many ExecutableDefinitions
//
//   - https://spec.graphql.org/October2021/#Document
//   - https://spec.graphql.org/October2021/#ExecutableDefinition
func ReadDocument(h Hash, s []byte) (err error) {
	s = SkipIgnorables(s)
	if err = ExpectNoEOF(s); err != nil {
		return err
	}
	for {
		if len(s) < 1 {
			return nil
		}
		if s, err = ReadDefinition(h, s); err != nil {
			return err
		}
		s = SkipIgnorables(s)
	}
}

// ReadDefinition reads Definition.
// Reference:
//
//   - https://spec.graphql.org/October2021/#Definition
func ReadDefinition(h Hash, s []byte) (suffix []byte, err error) {
	if err = ExpectNoEOF(s); err != nil {
		return s, err
	}
	switch {
	case s[0] == '{':
		// Anonymous operation.
		// (https://spec.graphql.org/October2021/#sec-Anonymous-Operation-Definitions)
		_, _ = h.Write(HPrefQuery)
		return ReadSelectionSet(h, s)

	case HasPrefix(s, "fragment"):
		// FragmentDefinition (https://spec.graphql.org/October2021/#FragmentDefinition).
		s = s[len("fragment"):]
		s = SkipIgnorables(s)

		// FragmentName (https://spec.graphql.org/October2021/#FragmentName).
		var name []byte
		if name, suffix, err = ReadName(s); err != nil {
			return suffix, err
		}
		if string(name) == "on" {
			return s, ErrUnexpectedToken // Return suffix as []byte.
		}

		// TypeCondition (https://spec.graphql.org/October2021/#TypeCondition).
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
		if _, suffix, err = ReadDirectives(h, suffix); err != nil {
			return suffix, err
		}
		suffix = SkipIgnorables(suffix)

		return ReadSelectionSet(h, suffix)
	}

	return ReadOperationDefinition(h, s)
}

// ReadOperationDefinition reads OperationDefinition
// but not the SelectionSet-only version of it.
// Reference:
//
//   - https://spec.graphql.org/October2021/#sec-Language.Operations
func ReadOperationDefinition(h Hash, s []byte) (suffix []byte, err error) {
	if _, s, err = ReadOperationType(h, s); err != nil {
		return s, err
	}
	s = SkipIgnorables(s)
	if err = ExpectNoEOF(s); err != nil {
		return s, err
	}

	// Optional name.
	if IsNameStart(s[0]) {
		var name []byte
		if name, s, err = ReadName(s); err != nil {
			return s, err
		}
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
		if s, err = ReadVariableDefinitionsAfterParenthesis(h, s); err != nil {
			return s, err
		}
		s = SkipIgnorables(s)
	}

	// Optional directives.
	if _, s, err = ReadDirectives(h, s); err != nil {
		return s, err
	}
	s = SkipIgnorables(s)

	return ReadSelectionSet(h, s)
}

// ReadVariableDefinitions reads VariableDefinitions.
// Reference:
//
//   - https://spec.graphql.org/October2021/#sec-Selection-Sets
func ReadSelectionSet(h Hash, s []byte) (suffix []byte, err error) {
	if s, err = ReadToken(s, "{"); err != nil {
		return s, err
	}
	s = SkipIgnorables(s)

	_, _ = h.Write(HPrefSelectionSet)

	for {
		if HasPrefix(s, "...") {
			// Fragment spread or inline fragment
			// (https://spec.graphql.org/October2021/#Selection).
			s = s[len("..."):]
			s = SkipIgnorables(s)

			if len(s) > len("on ") && HasPrefix(s, "on") && IsIgnorableByte(s[2]) {
				// Inline fragment (https://spec.graphql.org/October2021/#InlineFragment).

				// Type condition (https://spec.graphql.org/October2021/#TypeCondition).
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
				if _, s, err = ReadDirectives(h, s); err != nil {
					return s, err
				}

				s = SkipIgnorables(s)
				if s, err = ReadSelectionSet(h, s); err != nil { // Recurse.
					return s, err
				}
				s = SkipIgnorables(s)

			} else if len(s) > 0 && IsNameStart(s[0]) {
				// Fragment spread (https://spec.graphql.org/October2021/#FragmentSpread).

				// Fragment name (https://spec.graphql.org/October2021/#FragmentName).
				var fragName []byte
				if fragName, s, err = ReadName(s); err != nil {
					return s, err
				}
				_, _ = h.Write(HPrefFragmentSpread)
				_, _ = h.Write([]byte(fragName))
				s = SkipIgnorables(s)

				// Optional directives.
				if _, s, err = ReadDirectives(h, s); err != nil {
					return s, err
				}
				s = SkipIgnorables(s)
			} else {
				// Inline fragment without type condition.
				_, _ = h.Write(HPrefInlineFragment)
				if _, s, err = ReadDirectives(h, s); err != nil {
					return s, err
				}
				s = SkipIgnorables(s)
				if s, err = ReadSelectionSet(h, s); err != nil { // Recurse.
					return s, err
				}
				s = SkipIgnorables(s)
			}
		} else {
			// Field (https://spec.graphql.org/October2021/#Field).
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
				if _, s, err = ReadArguments(h, s); err != nil {
					return s, err
				}
				s = SkipIgnorables(s)
			}

			// Optional directives.
			if _, s, err = ReadDirectives(h, s); err != nil {
				return s, err
			}
			s = SkipIgnorables(s)

			// Optional selection set.
			if err = ExpectNoEOF(s); err != nil {
				return s, err
			}
			if s[0] == '{' {
				if s, err = ReadSelectionSet(h, s); err != nil { // Recurse.
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

// ReadVariableDefinitionsAfterParenthesis reads VariableDefinitions
// after '(' and any ignorables.
// Reference:
//
//   - https://spec.graphql.org/October2021/#VariableDefinitions
func ReadVariableDefinitionsAfterParenthesis(h Hash, s []byte) (suffix []byte, err error) {
	for {
		if s[0] != '$' {
			return s, ErrUnexpectedToken
		}
		s = SkipIgnorables(s[1:])
		var name []byte
		if name, s, err = ReadName(s); err != nil {
			return s, nil
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
			if _, _, s, err = ReadValue(h, s); err != nil {
				return s, err
			}
			s = SkipIgnorables(s)
		}

		// Optional directives.
		if _, s, err = ReadDirectives(h, s); err != nil {
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

// ReadDirectives reads Directives.
// Reference:
//
//   - https://spec.graphql.org/October2021/#sec-Language.Directives
func ReadDirectives(h Hash, s []byte) (directives, suffix []byte, err error) {
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
			if _, suffix, err = ReadArguments(h, suffix); err != nil {
				return directives, suffix, err
			}
		}
		suffix = SkipIgnorables(suffix)
	}
	return s[:len(s)-len(suffix)], suffix, nil
}

// ReadArguments reads Arguments.
// Reference:
//
//   - https://spec.graphql.org/October2021/#Arguments
func ReadArguments(h Hash, s []byte) (arguments, suffix []byte, err error) {
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
		if _, _, suffix, err = ReadValue(h, suffix); err != nil {
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

// ReadToken expects token to be prefix of s and returns []byte the token trimmed.
func ReadToken(s []byte, token string) (suffix []byte, err error) {
	if err = ExpectNoEOF(s); err != nil {
		return s, err
	}
	if !HasPrefix(s, token) {
		return s, ErrUnexpectedToken
	}
	return s[len(token):], nil
}

// ReadType reads Type.
// Reference:
//
//   - https://spec.graphql.org/October2021/#Type
func ReadType(s []byte) (typeDef []byte, nullable, array bool, suffix []byte, err error) {
	suffix, nullable = s, true
	if err = ExpectNoEOF(suffix); err != nil {
		return typeDef, nullable, array, suffix, err
	}
	switch {
	case IsNameStart(suffix[0]):
		if typeDef, suffix, err = ReadName(suffix); err != nil {
			return typeDef, nullable, array, suffix, err
		}
		// Continue.
	case suffix[0] == '[':
		array = true
		suffix = SkipIgnorables(suffix[1:])
		// Recurse.
		if _, _, _, suffix, err = ReadType(suffix); err != nil {
			return typeDef, nullable, array, suffix, err
		}
		suffix = SkipIgnorables(suffix)
		if err = ExpectNoEOF(suffix); err != nil {
			return typeDef, nullable, array, suffix, err
		}
		if suffix[0] != ']' {
			return typeDef, nullable, array, suffix, ErrUnexpectedToken
		}
		suffix = suffix[1:]
	default:
		return typeDef, nullable, array, suffix, ErrUnexpectedToken
	}
	{
		s := SkipIgnorables(suffix)
		if len(s) > 0 && s[0] == '!' {
			nullable, suffix = false, s[1:]
		}
	}
	return s[:len(s)-len(suffix)], nullable, array, suffix, err
}

// ValueType represents the type of a value
// Reference:
//
//   - https://spec.graphql.org/October2021/#Value
type ValueType int8

const (
	_ ValueType = iota
	ValueTypeInt
	ValueTypeFloat
	ValueTypeString
	ValueTypeStringBlock
	ValueTypeBooleanTrue
	ValueTypeBooleanFalse
	ValueTypeNull
	ValueTypeEnum
	ValueTypeList
	ValueTypeInputObject
	ValueTypeVariable
)

// ReadValue reads Value.
// Reference:
//
//   - https://spec.graphql.org/October2021/#Value
func ReadValue(h Hash, s []byte) (
	value []byte, valueType ValueType, suffix []byte, err error,
) {
	if err = ExpectNoEOF(s); err != nil {
		return value, valueType, s, err
	}
	switch {
	case HasPrefix(s, "null"):
		// NullValue (https://spec.graphql.org/October2021/#sec-Null-Value).
		_, _ = h.Write(HPrefValueNull)
		return s[:len("null")], ValueTypeNull, s[len("null"):], nil

	case HasPrefix(s, "true"):
		// BooleanValue (https://spec.graphql.org/October2021/#sec-Boolean-Value).
		_, _ = h.Write(HPrefValueTrue)
		return s[:len("true")], ValueTypeBooleanTrue, s[len("true"):], nil

	case HasPrefix(s, "false"):
		// BooleanValue (https://spec.graphql.org/October2021/#sec-Boolean-Value).
		_, _ = h.Write(HPrefValueFalse)
		return s[:len("false")], ValueTypeBooleanFalse, s[len("false"):], nil

	case s[0] == '$':
		// Variable (https://spec.graphql.org/October2021/#Variable).
		s = SkipIgnorables(s[1:])
		if _, suffix, err = ReadName(s); err != nil {
			return s, ValueTypeVariable, suffix, err
		}
		value = s[:len(s)-len(suffix)]
		_, _ = h.Write(HPrefValueVariable)
		_, _ = h.Write([]byte(value))
		return value, ValueTypeVariable, suffix, err

	case s[0] == '-' || IsDigit(s[0]):
		if value, suffix, err = ReadIntValue(s); err != nil {
			return value, ValueTypeInt, suffix, err
		}
		if len(suffix) > 0 && suffix[0] == '.' {
			if _, suffix, err = ReadFloatEnd(suffix[1:]); err != nil {
				return value, ValueTypeFloat, suffix, err
			}
			// FloatValue (https://spec.graphql.org/October2021/#sec-Float-Value).
			_, _ = h.Write(HPrefValueFloat)
			_, _ = h.Write([]byte(value))
			return s[:len(s)-len(suffix)], ValueTypeFloat, suffix, nil
		}
		// IntValue (https://spec.graphql.org/October2021/#sec-Int-Value).
		_, _ = h.Write(HPrefValueInteger)
		_, _ = h.Write([]byte(value))
		return value, ValueTypeInt, suffix, nil

	case s[0] == '"': // String or block string.
		if HasPrefix(s, `"""`) { // Block string.
			if _, suffix, err = ReadStringBlockAfterQuotes(s[3:]); err != nil {
				return value, ValueTypeStringBlock, suffix, err
			}
			value = s[:len(s)-len(suffix)]
			_, _ = h.Write(HPrefValueStringBlock)
			_, _ = h.Write([]byte(value))
			return value, ValueTypeStringBlock, suffix, nil
		} else { // String.
			if _, suffix, err = ReadStringLineAfterQuotes(s[1:]); err != nil {
				return value, ValueTypeString, suffix, err
			}
			value = s[:len(s)-len(suffix)]
			_, _ = h.Write(HPrefValueString)
			_, _ = h.Write([]byte(value))
			return value, ValueTypeString, suffix, nil
		}

	case s[0] == '[':
		// ListValue (https://spec.graphql.org/October2021/#sec-List-Value).
		_, _ = h.Write(HPrefValueList)
		suffix = SkipIgnorables(s[1:])
		if len(suffix) > 0 && suffix[0] == ']' {
			suffix = suffix[1:]
			return s[:len(s)-len(suffix)], ValueTypeList, suffix, nil
		}
		for len(suffix) > 0 {
			if _, _, suffix, err = ReadValue(h, suffix); err != nil {
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
		// InputObject (https://spec.graphql.org/October2021/#sec-Input-Object-Values).
		_, _ = h.Write(HPrefValueInputObject)
		suffix = SkipIgnorables(s[1:])
		if len(suffix) > 0 && suffix[0] == '}' {
			suffix = suffix[1:]
			return s[:len(s)-len(suffix)], ValueTypeInputObject, suffix, nil
		}
		for len(suffix) > 0 {
			// ObjectField (https://spec.graphql.org/October2021/#ObjectField).
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
			if _, _, suffix, err = ReadValue(h, suffix); err != nil {
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
		// EnumValue (https://spec.graphql.org/October2021/#sec-Enum-Value).
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

// ReadIntValue reads IntValue.
// Reference:
//
//   - https://spec.graphql.org/October2021/#IntValue
func ReadIntValue(s []byte) (value []byte, suffix []byte, err error) {
	suffix = s
	if suffix[0] == '-' {
		// Negative integer.
		suffix = suffix[1:]
		if err = ExpectNoEOF(suffix); err != nil {
			return value, s, err
		}
	}
	if suffix[0] == '0' {
		// Zero.
		suffix = suffix[1:]
		return s[:len(s)-len(suffix)], suffix, nil
	}
	for ; len(suffix) > 0 && IsDigit(suffix[0]); suffix = suffix[1:] {
	}
	return s[:len(s)-len(suffix)], suffix, nil
}

// ReadStringLineAfterQuotes reads a single-line StringValue contents after '"'.
// Tip: Use ReadStringBlock for block strings.
// Reference:
//
//   - https://spec.graphql.org/October2021/#sec-String-Value
func ReadStringLineAfterQuotes(s []byte) (value []byte, suffix []byte, err error) {
	for i := 0; i < len(s); {
		switch s[i] {
		case '"': // End of string.
			return s[:i], s[i+1:], nil
		case '\\':
			// EscapedCharacter (https://spec.graphql.org/October2021/#EscapedCharacter).
			if i+1 >= len(s) {
				return s[:i], s[i:], ErrUnexpectedEOF
			}
			switch s[i+1] {
			case '"', '\\', '/', 'b', 'f', 'n', 'r', 't':
				i += 2
			case 'u':
				// EscapedUnicode (https://spec.graphql.org/October2021/#EscapedUnicode).
				if i+5 >= len(s) {
					return s[:i+1], s[i+1:], ErrUnexpectedEOF
				}
				if !IsHexByte(s[i+2]) ||
					!IsHexByte(s[i+3]) ||
					!IsHexByte(s[i+4]) ||
					!IsHexByte(s[i+5]) {
					return s[:i+1], s[i+1:], ErrUnexpectedToken
				}
				i += 5
			default:
				return s, s[i:], ErrUnexpectedToken
			}
		default:
			i++
		}
	}
	return s, suffix, ErrUnexpectedEOF
}

// ReadStringBlockAfterQuotes reads a block string StringValue contents after '"""'.
// Tip: Use ReadStringLineAfterQuotes for single-line strings.
// Reference:
//
//   - https://spec.graphql.org/October2021/#sec-String-Value
func ReadStringBlockAfterQuotes(s []byte) (value []byte, suffix []byte, err error) {
	for i := 0; i < len(s); {
		switch s[i] {
		case '"': // End of string.
			if i+2 < len(s) && s[i+1] == '"' && s[i+2] == '"' {
				return s[:i+3], s[i+3:], nil
			}
			i += 1
		case '\\':
			// EscapedCharacter (https://spec.graphql.org/October2021/#EscapedCharacter).
			if i+1 >= len(s) {
				return s[:i], s[i:], ErrUnexpectedEOF
			}
			switch s[i+1] {
			case '"', '\\', '/', 'b', 'f', 'n', 'r', 't':
				i += 2
			case 'u':
				// EscapedUnicode (https://spec.graphql.org/October2021/#EscapedUnicode).
				if i+5 >= len(s) {
					return s[:i+1], s[i+1:], ErrUnexpectedEOF
				}
				if !IsHexByte(s[i+2]) ||
					!IsHexByte(s[i+3]) ||
					!IsHexByte(s[i+4]) ||
					!IsHexByte(s[i+5]) {
					return s[:i+1], s[i+1:], ErrUnexpectedToken
				}
				i += 5
			default:
				return s, s[i:], ErrUnexpectedToken
			}
		default:
			i++
		}
	}
	return s, suffix, ErrUnexpectedEOF
}

// ReadFloatEnd reads the part of the FloatValue that comes after the first IntegerPart.
// Reference:
//
//   - https://spec.graphql.org/October2021/#sec-Float-Value
func ReadFloatEnd(s []byte) (value []byte, suffix []byte, err error) {
	// Fractional part.
	if len(s) > 0 && (s[0] == 'e' || s[0] == 'E') {
		return value, s, ErrUnexpectedToken
	}
	suffix = s
	for ; len(suffix) > 0 && IsDigit(suffix[0]); suffix = suffix[1:] {
	}
	if len(suffix) > 0 && (suffix[0] == 'e' || suffix[0] == 'E') {
		// Exponential part.
		suffix = suffix[1:]
		if len(suffix) > 0 && (suffix[0] == '-' || suffix[0] == '+') {
			// Signed exponential part.
			suffix = suffix[1:]
		}
		for ; len(suffix) > 0 && IsDigit(suffix[0]); suffix = suffix[1:] {
		}
	}
	return s[:len(s)-len(suffix)], suffix, nil
}

type OperationType int8

const (
	_ OperationType = iota
	OperationTypeQuery
	OperationTypeMutation
	OperationTypeSubscription
)

// ReadOperationType reads OperationType.
// Reference:
//
//   - https://spec.graphql.org/October2021/#OperationType
func ReadOperationType(h Hash, s []byte) (
	operationType OperationType, suffix []byte, err error,
) {
	if HasPrefix(s, "query") || s[0] == '{' {
		_, _ = h.Write(HPrefQuery)
		return OperationTypeQuery, s[len("query"):], nil
	} else if HasPrefix(s, "mutation") {
		_, _ = h.Write(HPrefMutation)
		return OperationTypeMutation, s[len("mutation"):], nil
	} else if HasPrefix(s, "subscription") {
		_, _ = h.Write(HPrefSubscription)
		return OperationTypeSubscription, s[len("subscription"):], nil
	}
	return 0, s, ErrUnexpectedToken
}

// SkipIgnorables []bytekips over any comments, spaces, tabs, line-breaks and
// carriage-returns it encounters and returns the s []byteuffix.
// Reference:
//
//   - https://spec.graphql.org/October2021/#sec-Line-Terminators
//   - https://spec.graphql.org/October2021/#sec-Comments
//   - https://spec.graphql.org/October2021/#sec-White-Space
func SkipIgnorables(s []byte) []byte {
	for len(s) > 0 {
		switch s[0] {
		case ' ', ',', '\t', '\n', '\r':
			s = s[1:]
		case '#':
			i := 1
			for ; i < len(s) && s[i] != '\n'; i++ {
			}
			s = s[i:]
		default:
			return s
		}
	}
	return s
}

// ExpectNoEOF returns ErrUnexpectedEOF if s is empty,
// otherwise returns nil.
func ExpectNoEOF(s []byte) error {
	if len(s) < 1 {
		return ErrUnexpectedEOF
	}
	return nil
}

// HasPrefix is equivalent to strings.HasPrefix and bytes.HasPrefix
// except that it works for both string and []byte.
func HasPrefix(s []byte, prefix string) bool {
	return len(s) >= len(prefix) && string(s[0:len(prefix)]) == string(prefix)
}

// ReadName reads a Name token.
// Reference:
//
//   - https://spec.graphql.org/October2021/#sec-Names
func ReadName(s []byte) (name, suffix []byte, err error) {
	if len(s) < 1 {
		return name, suffix, ErrUnexpectedEOF
	}
	if !IsNameStart(s[0]) {
		return name, s, ErrUnexpectedToken
	}
	for suffix = s[1:]; len(suffix) > 0; {
		if IsLetter(suffix[0]) || IsDigit(suffix[0]) || suffix[0] == '_' {
			suffix = suffix[1:]
			continue
		}
		break
	}
	return s[:len(s)-len(suffix)], suffix, nil
}

// IsIgnorableByte returns true if b is ignorable.
// Reference:
//
//   - https://spec.graphql.org/October2021/#sec-Language.Source-Text.Ignored-Tokens
func IsIgnorableByte(b byte) bool {
	return b == ' ' || b == ',' || b == '\t' || b == '\n' || b == '\r'
}

// IsNameStart returns true if b is NameStart.
// Reference:
//
//   - https://spec.graphql.org/October2021/#NameStart
func IsNameStart(b byte) bool { return lutLetter[b] || b == '_' }

// IsLetter returns true if b is Letter.
// Reference:
//
//   - https://spec.graphql.org/October2021/#Letter
func IsLetter(b byte) bool { return lutLetter[b] }

// IsDigit returns true if b is a Digit.
// Reference:
//
//   - https://spec.graphql.org/October2021/#Digit
func IsDigit(b byte) bool { return lutDigit[b] }

// IsHexByte returns true if b is a hexadecimal digit.
func IsHexByte(b byte) bool { return lutHex[b] }

// lutLetter is a lookup table for bytes representing a Letter.
// Reference:
//
//   - https://spec.graphql.org/October2021/#Letter
var lutLetter = [256]bool{
	// Upper case.
	'A': true,
	'B': true,
	'C': true,
	'D': true,
	'E': true,
	'F': true,
	'G': true,
	'H': true,
	'I': true,
	'J': true,
	'K': true,
	'L': true,
	'M': true,
	'N': true,
	'O': true,
	'P': true,
	'Q': true,
	'R': true,
	'S': true,
	'T': true,
	'U': true,
	'V': true,
	'W': true,
	'X': true,
	'Y': true,
	'Z': true,
	// Lower case.
	'a': true,
	'b': true,
	'c': true,
	'd': true,
	'e': true,
	'f': true,
	'g': true,
	'h': true,
	'i': true,
	'j': true,
	'k': true,
	'l': true,
	'm': true,
	'n': true,
	'o': true,
	'p': true,
	'q': true,
	'r': true,
	's': true,
	't': true,
	'u': true,
	'v': true,
	'w': true,
	'x': true,
	'y': true,
	'z': true,
}

// lutDigit is a lookup table for bytes representing a Digit.
// Reference:
//
//   - https://spec.graphql.org/October2021/#Digit
var lutDigit = [256]bool{
	'0': true,
	'1': true,
	'2': true,
	'3': true,
	'4': true,
	'5': true,
	'6': true,
	'7': true,
	'8': true,
	'9': true,
}

// lutHex is a lookup table for hexadecimal digits.
// Reference:
//
//   - https://spec.graphql.org/October2021/#EscapedUnicode
var lutHex = [256]bool{
	'0': true,
	'1': true,
	'2': true,
	'3': true,
	'4': true,
	'5': true,
	'6': true,
	'7': true,
	'8': true,
	'9': true,
	'a': true,
	'b': true,
	'c': true,
	'd': true,
	'e': true,
	'f': true,
	'A': true,
	'B': true,
	'C': true,
	'D': true,
	'E': true,
	'F': true,
}
