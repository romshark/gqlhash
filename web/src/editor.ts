// CodeMirror setup: a GraphQL editor that takes its colors from the page and
// underlines where the gqlhash parser stopped.

import { indentWithTab } from "@codemirror/commands";
import { HighlightStyle, syntaxHighlighting } from "@codemirror/language";
import { type Diagnostic, setDiagnostics } from "@codemirror/lint";
import { Compartment, EditorState, type Extension } from "@codemirror/state";
import { EditorView, keymap } from "@codemirror/view";
import { tags } from "@lezer/highlight";
import { basicSetup } from "codemirror";
import type { ParseError } from "./gqlhash.js";
import { graphql } from "./graphql-language.js";
import * as theme from "./theme.js";

// Which palette is in force is theme.ts's answer, not a media query's: the
// header's switch can overrule the system. Every editor state is built for the
// palette of the moment it's created in, and syncTheme puts a state that was
// built under the other one back in step when it's swapped into a view.

// The rules are the same in both schemes because every color is a CSS variable
// the stylesheet redefines. Only CodeMirror's own dark flag has to be swapped,
// which decides the defaults of the pieces it styles itself, so the theme lives
// in a compartment and is reconfigured when the system scheme changes.
const themeConfig = new Compartment();

const themeSpec = {
  "&": {
    color: "var(--fg)",
    backgroundColor: "transparent",
    height: "100%",
    fontSize: "14px",
  },
  ".cm-content": {
    caretColor: "var(--accent)",
    fontFamily: "var(--mono)",
    padding: "12px 0",
  },
  ".cm-scroller": { fontFamily: "var(--mono)", lineHeight: "1.6" },
  "&.cm-focused": { outline: "none" },
  ".cm-gutters": {
    backgroundColor: "transparent",
    color: "var(--fg-faint)",
    border: "none",
  },
  // basicSetup brings a fold gutter, which reserves a column of its own for
  // arrows that only appear on hover. The line numbers sit next to the code
  // instead, and folding is still on Ctrl/Cmd-Alt-[ and -]. The !important is
  // to beat CodeMirror's own, on the .cm-gutter class this element also has.
  ".cm-foldGutter": { display: "none !important" },
  ".cm-lineNumbers .cm-gutterElement": { padding: "0 10px 0 5px" },
  ".cm-activeLine": { backgroundColor: "var(--active-line)" },
  ".cm-activeLineGutter": {
    backgroundColor: "var(--active-line)",
    color: "var(--fg-muted)",
  },
  ".cm-selectionBackground, ::selection": {
    backgroundColor: "var(--selection) !important",
  },
  ".cm-cursor, .cm-dropCursor": { borderLeftColor: "var(--accent)" },
  ".cm-matchingBracket": {
    backgroundColor: "var(--selection)",
    outline: "none",
  },
  ".cm-tooltip": {
    backgroundColor: "var(--bg-raised)",
    border: "1px solid var(--border)",
    borderRadius: "6px",
    color: "var(--fg)",
  },
  ".cm-diagnostic-error": {
    borderLeftColor: "var(--error)",
    backgroundColor: "var(--bg-raised)",
  },
  ".cm-lintRange-error": {
    // The default squiggle is a background image tinted by currentColor.
    textDecoration: "underline wavy var(--error)",
    textUnderlineOffset: "3px",
    backgroundImage: "none",
  },
};

function buildTheme(dark: boolean): Extension {
  return EditorView.theme(themeSpec, { dark });
}

const highlight = HighlightStyle.define([
  { tag: tags.comment, color: "var(--syn-comment)", fontStyle: "italic" },
  { tag: tags.keyword, color: "var(--syn-keyword)" },
  { tag: tags.atom, color: "var(--syn-atom)" },
  { tag: tags.number, color: "var(--syn-number)" },
  { tag: tags.string, color: "var(--syn-string)" },
  { tag: tags.variableName, color: "var(--syn-field)" },
  { tag: tags.propertyName, color: "var(--syn-argument)" },
  { tag: tags.typeName, color: "var(--syn-type)" },
  { tag: tags.meta, color: "var(--syn-directive)" },
  { tag: tags.punctuation, color: "var(--fg-muted)" },
]);

/**
 * createEditorState builds the state of one file. Each file keeps its own, so
 * switching tabs keeps its cursor, scroll position and undo history — the view
 * they're shown in is shared and holds only whichever one is on screen.
 *
 * onChange fires for every document edit, undebounced.
 */
export function createEditorState(
  doc: string,
  onChange: (doc: string) => void,
): EditorState {
  const extensions: Extension[] = [
    basicSetup,
    keymap.of([indentWithTab]),
    graphql,
    syntaxHighlighting(highlight),
    themeConfig.of(buildTheme(theme.dark())),
    EditorView.lineWrapping,
    EditorView.updateListener.of((update) => {
      if (update.docChanged) {
        onChange(update.state.doc.toString());
      }
    }),
  ];
  return EditorState.create({ doc, extensions });
}

export function createEditor(
  parent: HTMLElement,
  state: EditorState,
): EditorView {
  const view = new EditorView({ parent, state });

  // The stylesheet follows the palette on its own; this keeps the parts
  // CodeMirror styles itself in step when it changes while the page is open.
  theme.onChange(() => syncTheme(view));

  return view;
}

/**
 * syncTheme reconfigures the view for the palette in force now. A state
 * carries the theme it was created with, so this is what a state built before
 * the last switch needs when it's swapped in.
 */
export function syncTheme(view: EditorView): void {
  view.dispatch({
    effects: themeConfig.reconfigure(buildTheme(theme.dark())),
  });
}

/**
 * showError marks the position the parser stopped at. Passing null clears the
 * marker, and so does an error that carries no position: pointing at a line the
 * parser didn't name would claim the problem is somewhere it isn't. The message
 * is shown next to the hash either way.
 */
export function showError(view: EditorView, error: ParseError | null): void {
  const diagnostics: Diagnostic[] = [];
  const from = error ? positionOf(view, error) : null;
  if (error && from !== null) {
    diagnostics.push({
      from,
      to: Math.min(from + 1, view.state.doc.length),
      severity: "error",
      message: error.message,
    });
  }
  view.dispatch(setDiagnostics(view.state, diagnostics));
}

/**
 * positionOf converts the parser's 1-based line and character column into a
 * CodeMirror offset, which counts UTF-16 code units rather than characters.
 * Returns null for an error that doesn't say where it stopped.
 */
function positionOf(view: EditorView, error: ParseError): number | null {
  const doc = view.state.doc;
  if (!error.line) {
    return null;
  }
  if (error.line < 1 || error.line > doc.lines) {
    return doc.length;
  }
  const line = doc.line(error.line);
  const column = Math.max((error.column ?? 1) - 1, 0);
  // Slicing by code points keeps the column correct for astral characters.
  const prefix = Array.from(line.text).slice(0, column).join("");
  return Math.min(line.from + prefix.length, line.to);
}
