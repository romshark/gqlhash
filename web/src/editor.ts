// CodeMirror setup: a GraphQL editor that takes its colors from the page and a
// gutter that marks where the gqlhash parser stopped.

import { indentWithTab } from "@codemirror/commands";
import { HighlightStyle, syntaxHighlighting } from "@codemirror/language";
import { type Diagnostic, lintGutter, setDiagnostics } from "@codemirror/lint";
import { Compartment, EditorState, type Extension } from "@codemirror/state";
import { EditorView, keymap } from "@codemirror/view";
import { tags } from "@lezer/highlight";
import { basicSetup } from "codemirror";
import type { ParseError } from "./gqlhash.js";
import { graphql } from "./graphql-language.js";

const darkQuery = "(prefers-color-scheme: dark)";

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

export interface EditorOptions {
  readonly parent: HTMLElement;
  readonly doc: string;
  /** onChange fires for every document edit, undebounced. */
  readonly onChange: (doc: string) => void;
}

export function createEditor(options: EditorOptions): EditorView {
  const media = window.matchMedia(darkQuery);

  const extensions: Extension[] = [
    basicSetup,
    keymap.of([indentWithTab]),
    graphql,
    syntaxHighlighting(highlight),
    lintGutter(),
    themeConfig.of(buildTheme(media.matches)),
    EditorView.lineWrapping,
    EditorView.updateListener.of((update) => {
      if (update.docChanged) {
        options.onChange(update.state.doc.toString());
      }
    }),
  ];

  const view = new EditorView({
    parent: options.parent,
    state: EditorState.create({ doc: options.doc, extensions }),
  });

  // The stylesheet follows the system scheme on its own; this keeps the parts
  // CodeMirror styles itself in step when the scheme changes while the page is
  // open.
  media.addEventListener("change", (event) => {
    view.dispatch({
      effects: themeConfig.reconfigure(buildTheme(event.matches)),
    });
  });

  return view;
}

/** setDocument replaces the whole document, e.g. to restore the example. */
export function setDocument(view: EditorView, doc: string): void {
  view.dispatch({
    changes: { from: 0, to: view.state.doc.length, insert: doc },
    selection: { anchor: Math.min(doc.length, view.state.doc.length) },
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
