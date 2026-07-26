// A small GraphQL mode for CodeMirror. It only highlights, which is all this
// page needs: completion and validation would require a schema, and the actual
// syntax checking is done by the gqlhash parser itself.

import { StreamLanguage, type StringStream } from "@codemirror/language";

const keywords = new Set([
  "query",
  "mutation",
  "subscription",
  "fragment",
  "on",
  "directive",
  "schema",
  "scalar",
  "type",
  "interface",
  "union",
  "enum",
  "input",
  "extend",
  "implements",
  "repeatable",
]);

const atoms = new Set(["true", "false", "null"]);

const nameStart = /[_A-Za-z]/;
const nameChar = /[_0-9A-Za-z]/;

interface State {
  /** inBlockString is true while a `"""` string spans into the next line. */
  inBlockString: boolean;
  /** afterColon is true right after a `:`, where a type name is expected. */
  afterColon: boolean;
}

export const graphql = StreamLanguage.define<State>({
  name: "graphql",

  startState: () => ({ inBlockString: false, afterColon: false }),

  token(stream: StringStream, state: State): string | null {
    if (state.inBlockString) {
      return blockString(stream, state);
    }
    if (stream.eatSpace()) {
      return null;
    }

    const afterColon = state.afterColon;
    state.afterColon = false;

    // Comment.
    if (stream.eat("#")) {
      stream.skipToEnd();
      return "comment";
    }

    // Block string.
    if (stream.match('"""')) {
      state.inBlockString = true;
      return blockString(stream, state);
    }

    // String. An unterminated one runs to the end of the line, which is what
    // the GraphQL grammar says it is: still a string, just an invalid one.
    if (stream.eat('"')) {
      let escaped = false;
      for (;;) {
        const ch = stream.next();
        if (ch === undefined) {
          break;
        }
        if (escaped) {
          escaped = false;
        } else if (ch === "\\") {
          escaped = true;
        } else if (ch === '"') {
          break;
        }
      }
      return "string";
    }

    // Variable.
    if (stream.eat("$")) {
      stream.eatWhile(nameChar);
      return "variableName";
    }

    // Directive.
    if (stream.eat("@")) {
      stream.eatWhile(nameChar);
      return "meta";
    }

    // Number.
    if (stream.match(/^-?(?:0|[1-9]\d*)(?:\.\d+)?(?:[eE][+-]?\d+)?/)) {
      return "number";
    }

    // Name: keyword, enum-ish atom, type name or field/argument name.
    if (stream.match(nameStart)) {
      stream.eatWhile(nameChar);
      const word = stream.current();
      if (keywords.has(word)) {
        return "keyword";
      }
      if (atoms.has(word)) {
        return "atom";
      }
      if (afterColon) {
        return "typeName";
      }
      // A name followed by a colon labels an argument or aliases a field.
      return stream.match(/^\s*:/, false) ? "propertyName" : "variableName";
    }

    // Punctuation.
    if (stream.eat(":")) {
      state.afterColon = true;
      return "punctuation";
    }
    if (stream.match("...") || stream.match(/^[{}()[\]=|!&]/)) {
      return "punctuation";
    }

    // Anything else is not valid GraphQL; let the parser report it.
    stream.next();
    return null;
  },

  languageData: {
    commentTokens: { line: "#" },
    closeBrackets: { brackets: ["(", "[", "{", '"', '"""'] },
    indentOnInput: /^\s*[})\]]$/,
  },
});

/** blockString consumes the rest of a `"""` string on the current line. */
function blockString(stream: StringStream, state: State): string {
  while (!stream.eol()) {
    if (stream.match('\\"""')) {
      continue;
    }
    if (stream.match('"""')) {
      state.inBlockString = false;
      return "string";
    }
    stream.next();
  }
  return "string";
}
