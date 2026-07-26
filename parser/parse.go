package parser

import "io"

// Frames of the value stack, one per open ListValue or InputObjectValue.
const (
	frameList   = 1
	frameObject = 2
)

// Where the directives, arguments and values of a production continue once
// they're read.
//
// Why one variable per kind instead of a return stack: none of the three nests
// within itself.
const (
	// retDirSelectionSet is the directives of an OperationDefinition, a
	// FragmentDefinition or an InlineFragment. A SelectionSet follows all three.
	retDirSelectionSet = iota
	retDirVarDef
	retDirField
	retDirFragmentSpread
)

const (
	retArgField = iota
	retArgDirective
)

const (
	retValArgument = iota
	retValVarDefDefault
)

// parse reads a Document. Grammar productions are labels, transitions between
// them are gotos.
//
// SelectionSet, ListValue and InputObjectValue are the only productions that
// nest. Selection sets are tracked by depth, values by the stack of p.
//
// Why a flat state machine: reading a document costs no function calls and no
// stack frames beyond the leaf scanners.
//
// Reference:
//
//   - https://spec.graphql.org/September2025/#Document
func parse(p *state, dst io.Writer, o Options, src string) Error {
	// [Options.IgnoreVariables] is a superset of [Options.IgnoreInputs].
	if o.IgnoreVariables {
		o.IgnoreInputs = true
	}

	var (
		w     = writer{dst: dst, buf: p.buf[:0]}
		stack = p.stack[:0]

		i      int   // Index of the byte to read next.
		j      int   // Index of a byte read ahead of i.
		start  int   // Start of the token being read.
		e      error // Sentinel error, set before goto ERROR.
		errPos int   // Offset the error is reported at.

		selDepth  int // Number of SelectionSets currently open.
		typeDepth int // Number of ListType brackets currently open.

		dirRet uint8 // Where the directives currently read continue.
		argRet uint8 // Where the arguments currently read continue.
		valRet uint8 // Where the outermost value currently read continues.

		// constant marks a Value[Const], which admits no variable usage
		// (https://spec.graphql.org/September2025/#Value).
		constant bool

		described  bool // Whether the definition being read has a Description.
		isFloat    bool
		esc        bool
		hasContent bool
		prefixLen  int
	)

	i = skipIgnorables(src, 0)
	if i == len(src) {
		// A Document holds at least one Definition.
		e, errPos = ErrUnexpectedEOF, i
		goto ERROR
	}

DEFINITION:
	// Description, read and discarded
	// (https://spec.graphql.org/September2025/#sec-Descriptions).
	//
	// Requirement: a description must not affect execution.
	described = false
	if src[i] == '"' {
		described = true
		if hasPrefixAt(src, i, `"""`) {
			i, _, _, errPos, e = scanStringBlock(src, i+3)
		} else {
			i, _, errPos, e = scanStringLine(src, i+1)
		}
		if e != nil {
			goto ERROR
		}
		i = skipIgnorables(src, i)
		if i == len(src) {
			e, errPos = ErrUnexpectedEOF, i
			goto ERROR
		}
	}

	if src[i] == '{' {
		// Anonymous operation
		// (https://spec.graphql.org/September2025/#sec-Anonymous-Operation-Definitions).
		if described {
			// Query shorthand takes no Description: it has no OperationType.
			e, errPos = ErrUnexpectedToken, i
			goto ERROR
		}
		w.pref(HPrefQuery)
		goto SEL_SET
	}
	if isKeywordAt(src, i, "fragment") {
		goto FRAGMENT
	}

	// OperationDefinition
	// (https://spec.graphql.org/September2025/#sec-Language.Operations).
	switch {
	case isKeywordAt(src, i, "query"):
		w.pref(HPrefQuery)
		i += len("query")
	case isKeywordAt(src, i, "mutation"):
		w.pref(HPrefMutation)
		i += len("mutation")
	case isKeywordAt(src, i, "subscription"):
		w.pref(HPrefSubscription)
		i += len("subscription")
	default:
		e, errPos = ErrUnexpectedToken, i
		goto ERROR
	}
	i = skipIgnorables(src, i)
	if i == len(src) {
		e, errPos = ErrUnexpectedEOF, i
		goto ERROR
	}

	// Optional name.
	if lutNameStart[src[i]] {
		i = w.nameStr(src, i)
		i = skipIgnorables(src, i)
		if i == len(src) {
			e, errPos = ErrUnexpectedEOF, i
			goto ERROR
		}
	}

	// Optional VariableDefinitions
	// (https://spec.graphql.org/September2025/#VariableDefinitions).
	if src[i] == '(' {
		i = skipIgnorables(src, i+1)
		if i == len(src) {
			e, errPos = ErrUnexpectedEOF, i
			goto ERROR
		}
		if o.IgnoreVariables {
			w.mute++
		}
		goto VARDEF
	}
	constant = false
	dirRet = retDirSelectionSet
	goto DIRECTIVES

DEFINITION_END:
	i = skipIgnorables(src, i)
	if i == len(src) {
		goto DONE
	}
	goto DEFINITION

FRAGMENT:
	// FragmentDefinition
	// (https://spec.graphql.org/September2025/#FragmentDefinition).
	i = skipIgnorables(src, i+len("fragment"))

	// FragmentName (https://spec.graphql.org/September2025/#FragmentName).
	if i == len(src) {
		e, errPos = ErrUnexpectedEOF, i
		goto ERROR
	}
	if !lutNameStart[src[i]] {
		e, errPos = ErrUnexpectedToken, i
		goto ERROR
	}
	start = i
	i = nameEnd(src, i+1)
	if src[start:i] == "on" {
		// A FragmentName is any Name but "on", which begins the TypeCondition.
		e, errPos = ErrUnexpectedToken, start
		goto ERROR
	}
	w.tok(HPrefFragmentDefinition, src[start:i])

	// TypeCondition (https://spec.graphql.org/September2025/#TypeCondition).
	i = skipIgnorables(src, i)
	if i == len(src) {
		e, errPos = ErrUnexpectedEOF, i
		goto ERROR
	}
	if !isKeywordAt(src, i, "on") {
		e, errPos = ErrUnexpectedToken, i
		goto ERROR
	}
	i = skipIgnorables(src, i+len("on"))
	if i == len(src) {
		e, errPos = ErrUnexpectedEOF, i
		goto ERROR
	}
	if !lutNameStart[src[i]] {
		e, errPos = ErrUnexpectedToken, i
		goto ERROR
	}
	i = w.nameTok(HPrefType, src, i)
	i = skipIgnorables(src, i)
	constant = false
	dirRet = retDirSelectionSet
	goto DIRECTIVES

VARDEF:
	// VariableDefinition (https://spec.graphql.org/September2025/#VariableDefinition).
	// Both entry points check for EOF, so one byte is left to read.
	if src[i] == '"' {
		// A variable definition takes a Description too.
		if hasPrefixAt(src, i, `"""`) {
			i, _, _, errPos, e = scanStringBlock(src, i+3)
		} else {
			i, _, errPos, e = scanStringLine(src, i+1)
		}
		if e != nil {
			goto ERROR
		}
		i = skipIgnorables(src, i)
		if i == len(src) {
			e, errPos = ErrUnexpectedEOF, i
			goto ERROR
		}
	}
	if src[i] != '$' {
		e, errPos = ErrUnexpectedToken, i
		goto ERROR
	}
	i = skipIgnorables(src, i+1)
	if i == len(src) {
		e, errPos = ErrUnexpectedEOF, i
		goto ERROR
	}
	if !lutNameStart[src[i]] {
		e, errPos = ErrUnexpectedToken, i
		goto ERROR
	}
	i = w.nameTok(HPrefVariableDefinition, src, i)

	i = skipIgnorables(src, i)
	if i == len(src) {
		e, errPos = ErrUnexpectedEOF, i
		goto ERROR
	}
	if src[i] != ':' {
		e, errPos = ErrUnexpectedToken, i
		goto ERROR
	}
	i = skipIgnorables(src, i+1)
	w.pref(HPrefType)
	typeDepth = 0
	goto TYPE

TYPE:
	// Type (https://spec.graphql.org/September2025/#Type). Only the names,
	// brackets and '!' are written, not the Ignored tokens between them.
	if i == len(src) {
		e, errPos = ErrUnexpectedEOF, i
		goto ERROR
	}
	switch {
	case lutNameStart[src[i]]:
		i = w.nameStr(src, i)
	case src[i] == '[':
		typeDepth++
		w.pref('[')
		i = skipIgnorables(src, i+1)
		goto TYPE
	default:
		e, errPos = ErrUnexpectedToken, i
		goto ERROR
	}

TYPE_AFTER:
	// NonNullType, whose '!' an Ignored token may precede.
	j = skipIgnorables(src, i)
	if j < len(src) && src[j] == '!' {
		w.pref('!')
		i = j + 1
	}
	if typeDepth > 0 {
		j = skipIgnorables(src, i)
		if j == len(src) {
			e, errPos = ErrUnexpectedEOF, j
			goto ERROR
		}
		if src[j] != ']' {
			e, errPos = ErrUnexpectedToken, j
			goto ERROR
		}
		w.pref(']')
		i = j + 1
		typeDepth--
		goto TYPE_AFTER
	}

	// The default value and the directives of a variable definition take
	// Value[Const] (https://spec.graphql.org/September2025/#VariableDefinition).
	i = skipIgnorables(src, i)
	if i == len(src) {
		e, errPos = ErrUnexpectedEOF, i
		goto ERROR
	}
	if src[i] == '=' {
		i = skipIgnorables(src, i+1)
		constant = true
		valRet = retValVarDefDefault
		if o.IgnoreInputs {
			w.mute++
		}
		goto VALUE
	}
	goto VARDEF_DIRECTIVES

VARDEF_AFTER_DEFAULT:
	if o.IgnoreInputs {
		w.mute--
	}
	i = skipIgnorables(src, i)

VARDEF_DIRECTIVES:
	constant = true
	dirRet = retDirVarDef
	goto DIRECTIVES

VARDEF_END:
	i = skipIgnorables(src, i)
	if i == len(src) {
		e, errPos = ErrUnexpectedEOF, i
		goto ERROR
	}
	if src[i] != ')' {
		goto VARDEF
	}
	if o.IgnoreVariables {
		w.mute--
	}
	i = skipIgnorables(src, i+1)
	constant = false
	dirRet = retDirSelectionSet
	goto DIRECTIVES

DIRECTIVES:
	// Directives are optional wherever they appear
	// (https://spec.graphql.org/September2025/#sec-Language.Directives).
	for i < len(src) && src[i] == '@' {
		i = skipIgnorables(src, i+1)
		if i == len(src) {
			e, errPos = ErrUnexpectedEOF, i
			goto ERROR
		}
		if !lutNameStart[src[i]] {
			e, errPos = ErrUnexpectedToken, i
			goto ERROR
		}
		i = w.nameTok(HPrefDirective, src, i)

		i = skipIgnorables(src, i)
		if i == len(src) {
			e, errPos = ErrUnexpectedEOF, i
			goto ERROR
		}
		if src[i] == '(' {
			argRet = retArgDirective
			goto ARGS
		}
	}
	switch dirRet {
	case retDirVarDef:
		goto VARDEF_END
	case retDirField:
		goto FIELD_AFTER_DIRECTIVES
	case retDirFragmentSpread:
		i = skipIgnorables(src, i)
		goto AFTER_SELECTION
	}
	// retDirSelectionSet: a SelectionSet follows.
	i = skipIgnorables(src, i)
	goto SEL_SET

ARGS:
	// Arguments (https://spec.graphql.org/September2025/#Arguments).
	i = skipIgnorables(src, i+1)

ARGS_NEXT:
	if i == len(src) {
		e, errPos = ErrUnexpectedEOF, i
		goto ERROR
	}
	if !lutNameStart[src[i]] {
		e, errPos = ErrUnexpectedToken, i
		goto ERROR
	}
	i = w.nameTok(HPrefArgument, src, i)

	i = skipIgnorables(src, i)
	if i == len(src) {
		e, errPos = ErrUnexpectedEOF, i
		goto ERROR
	}
	if src[i] != ':' {
		e, errPos = ErrUnexpectedToken, i
		goto ERROR
	}
	i = skipIgnorables(src, i+1)
	valRet = retValArgument
	if o.IgnoreInputs {
		w.mute++
	}
	goto VALUE

ARGS_AFTER_VALUE:
	if o.IgnoreInputs {
		w.mute--
	}
	i = skipIgnorables(src, i)
	if i == len(src) {
		e, errPos = ErrUnexpectedEOF, i
		goto ERROR
	}
	if src[i] != ')' {
		goto ARGS_NEXT
	}
	i++
	if argRet == retArgField {
		goto FIELD_AFTER_ARGS
	}
	i = skipIgnorables(src, i)
	goto DIRECTIVES

VALUE:
	// Value (https://spec.graphql.org/September2025/#Value).
	if i == len(src) {
		e, errPos = ErrUnexpectedEOF, i
		goto ERROR
	}
	switch src[i] {
	case '$':
		// Variable (https://spec.graphql.org/September2025/#Variable).
		if constant {
			// Value[Const] has no Variable, not even nested in a list or an
			// input object.
			e, errPos = ErrUnexpectedVariable, i
			goto ERROR
		}
		i = skipIgnorables(src, i+1)
		if i == len(src) {
			e, errPos = ErrUnexpectedEOF, i
			goto ERROR
		}
		if !lutNameStart[src[i]] {
			e, errPos = ErrUnexpectedToken, i
			goto ERROR
		}
		i = w.nameTok(HPrefValueVariable, src, i)
		goto AFTER_VALUE

	case '"':
		// StringValue (https://spec.graphql.org/September2025/#sec-String-Value).
		if hasPrefixAt(src, i, `"""`) {
			start = i + 3
			i, prefixLen, hasContent, errPos, e = scanStringBlock(src, start)
			if e != nil {
				goto ERROR
			}
			w.pref(HPrefValueString)
			if hasContent {
				// BlockStringValue joins the lines with a single line feed.
				w.blockStringValue(src[start:i-3], prefixLen)
			}
			goto AFTER_VALUE
		}
		start = i + 1
		i, esc, errPos, e = scanStringLine(src, start)
		if e != nil {
			goto ERROR
		}
		w.pref(HPrefValueString)
		w.stringValue(src[start:i-1], esc)
		goto AFTER_VALUE

	case '[':
		// ListValue (https://spec.graphql.org/September2025/#sec-List-Value).
		w.pref(HPrefValueList)
		i = skipIgnorables(src, i+1)
		if i < len(src) && src[i] == ']' {
			// No list end for an empty list: there are no items to separate.
			i++
			goto AFTER_VALUE
		}
		stack = append(stack, frameList)
		goto VALUE

	case '{':
		// InputObjectValue
		// (https://spec.graphql.org/September2025/#sec-Input-Object-Values).
		w.pref(HPrefValueInputObject)
		i = skipIgnorables(src, i+1)
		if i < len(src) && src[i] == '}' {
			// No object end for an empty input object: there are no fields.
			i++
			goto AFTER_VALUE
		}
		stack = append(stack, frameObject)
		goto OBJECT_FIELD

	case '-', '0', '1', '2', '3', '4', '5', '6', '7', '8', '9':
		// IntValue or FloatValue, which share the IntegerPart
		// (https://spec.graphql.org/September2025/#sec-Int-Value).
		start = i
		i, isFloat, errPos, e = scanNumber(src, i)
		if e != nil {
			goto ERROR
		}
		if isFloat {
			w.tok(HPrefValueFloat, src[start:i])
		} else {
			w.tok(HPrefValueInteger, src[start:i])
		}
		goto AFTER_VALUE
	}

	if isKeywordAt(src, i, "null") {
		// NullValue (https://spec.graphql.org/September2025/#sec-Null-Value).
		w.pref(HPrefValueNull)
		i += len("null")
		goto AFTER_VALUE
	}
	if isKeywordAt(src, i, "true") {
		// BooleanValue (https://spec.graphql.org/September2025/#sec-Boolean-Value).
		w.pref(HPrefValueTrue)
		i += len("true")
		goto AFTER_VALUE
	}
	if isKeywordAt(src, i, "false") {
		w.pref(HPrefValueFalse)
		i += len("false")
		goto AFTER_VALUE
	}
	// EnumValue (https://spec.graphql.org/September2025/#sec-Enum-Value).
	if !lutNameStart[src[i]] {
		e, errPos = ErrUnexpectedToken, i
		goto ERROR
	}
	i = w.nameTok(HPrefValueEnum, src, i)

AFTER_VALUE:
	if len(stack) == 0 {
		if valRet == retValArgument {
			goto ARGS_AFTER_VALUE
		}
		goto VARDEF_AFTER_DEFAULT
	}
	i = skipIgnorables(src, i)
	if i == len(src) {
		e, errPos = ErrUnexpectedEOF, i
		goto ERROR
	}
	if stack[len(stack)-1] == frameList {
		if src[i] != ']' {
			goto VALUE // The next item of the list.
		}
		w.pref(HPrefValueListEnd)
		i++
		stack = stack[:len(stack)-1]
		goto AFTER_VALUE
	}
	if src[i] != '}' {
		goto OBJECT_FIELD // The next field of the input object.
	}
	w.pref(HPrefInputObjectEnd)
	i++
	stack = stack[:len(stack)-1]
	goto AFTER_VALUE

OBJECT_FIELD:
	// ObjectField (https://spec.graphql.org/September2025/#ObjectField).
	if i == len(src) {
		e, errPos = ErrUnexpectedEOF, i
		goto ERROR
	}
	if !lutNameStart[src[i]] {
		e, errPos = ErrUnexpectedToken, i
		goto ERROR
	}
	i = w.nameTok(HPrefValueInputObjectField, src, i)

	i = skipIgnorables(src, i)
	if i == len(src) {
		e, errPos = ErrUnexpectedEOF, i
		goto ERROR
	}
	if src[i] != ':' {
		e, errPos = ErrUnexpectedToken, i
		goto ERROR
	}
	i = skipIgnorables(src, i+1)
	goto VALUE

SEL_SET:
	// SelectionSet (https://spec.graphql.org/September2025/#sec-Selection-Sets).
	if i == len(src) {
		e, errPos = ErrUnexpectedEOF, i
		goto ERROR
	}
	if src[i] != '{' {
		e, errPos = ErrUnexpectedToken, i
		goto ERROR
	}
	i = skipIgnorables(src, i+1)
	w.pref(HPrefSelectionSet)
	selDepth++

SELECTION:
	// Selection (https://spec.graphql.org/September2025/#Selection).
	if i == len(src) {
		e, errPos = ErrUnexpectedEOF, i
		goto ERROR
	}
	if src[i] == '.' && hasPrefixAt(src, i, "...") {
		i = skipIgnorables(src, i+len("..."))
		if isKeywordAt(src, i, "on") {
			// InlineFragment with a TypeCondition
			// (https://spec.graphql.org/September2025/#InlineFragment).
			// A bare "on" is unambiguous: no FragmentSpread is named "on".
			i = skipIgnorables(src, i+len("on"))
			if i == len(src) {
				e, errPos = ErrUnexpectedEOF, i
				goto ERROR
			}
			if !lutNameStart[src[i]] {
				e, errPos = ErrUnexpectedToken, i
				goto ERROR
			}
			w.pref(HPrefInlineFragment)
			i = w.nameTok(HPrefType, src, i)
			i = skipIgnorables(src, i)
			constant = false
			dirRet = retDirSelectionSet
			goto DIRECTIVES
		}
		if i < len(src) && lutNameStart[src[i]] {
			// FragmentSpread
			// (https://spec.graphql.org/September2025/#FragmentSpread).
			i = w.nameTok(HPrefFragmentSpread, src, i)
			i = skipIgnorables(src, i)
			constant = false
			dirRet = retDirFragmentSpread
			goto DIRECTIVES
		}
		// InlineFragment without a TypeCondition.
		w.pref(HPrefInlineFragment)
		constant = false
		dirRet = retDirSelectionSet
		goto DIRECTIVES
	}

	// Field (https://spec.graphql.org/September2025/#Field).
	if !lutNameStart[src[i]] {
		e, errPos = ErrUnexpectedToken, i
		goto ERROR
	}
	i = w.nameTok(HPrefField, src, i)

	i = skipIgnorables(src, i)
	if i == len(src) {
		e, errPos = ErrUnexpectedEOF, i
		goto ERROR
	}
	if src[i] == ':' {
		// The name above was an Alias
		// (https://spec.graphql.org/September2025/#Alias).
		i = skipIgnorables(src, i+1)
		if i == len(src) {
			e, errPos = ErrUnexpectedEOF, i
			goto ERROR
		}
		if !lutNameStart[src[i]] {
			e, errPos = ErrUnexpectedToken, i
			goto ERROR
		}
		i = w.nameTok(HPrefFieldAliasedName, src, i)
		i = skipIgnorables(src, i)
		if i == len(src) {
			e, errPos = ErrUnexpectedEOF, i
			goto ERROR
		}
	}

	// Optional arguments.
	if src[i] == '(' {
		argRet = retArgField
		constant = false
		goto ARGS
	}
	goto FIELD_DIRECTIVES

FIELD_AFTER_ARGS:
	i = skipIgnorables(src, i)

FIELD_DIRECTIVES:
	constant = false
	dirRet = retDirField
	goto DIRECTIVES

FIELD_AFTER_DIRECTIVES:
	i = skipIgnorables(src, i)
	if i == len(src) {
		e, errPos = ErrUnexpectedEOF, i
		goto ERROR
	}
	if src[i] == '{' {
		// Optional SelectionSet of the field.
		goto SEL_SET
	}

AFTER_SELECTION:
	if i == len(src) {
		e, errPos = ErrUnexpectedEOF, i
		goto ERROR
	}
	if src[i] != '}' {
		goto SELECTION // The next selection of this selection set.
	}
	w.pref(HPrefSelectionSetEnd)
	i++
	selDepth--
	if selDepth == 0 {
		goto DEFINITION_END
	}
	i = skipIgnorables(src, i)
	goto AFTER_SELECTION

DONE:
	e = w.flush()
	p.stack = stack
	if cap(w.buf) > maxRetainedBufferSize {
		// Why release it: one oversized document must not make the parser hold
		// on to an oversized buffer.
		w.buf = make([]byte, 0, DefaultBufferSize)
	}
	p.buf = w.buf
	if e != nil {
		// A write error is no syntax error and has no position in src.
		return Error{Err: e}
	}
	return Error{}

ERROR:
	p.stack = stack
	if cap(w.buf) > maxRetainedBufferSize {
		w.buf = make([]byte, 0, DefaultBufferSize)
	}
	p.buf = w.buf
	return newError(src, errPos, e)
}
