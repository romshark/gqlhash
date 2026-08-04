// What the page keeps between visits: the files in both panes and which of
// them was on screen.
//
// localStorage rather than IndexedDB. This is a few kilobytes of text read
// once, before the editors are built — IndexedDB buys size and structure that
// a handful of documents don't need, and charges for them by being
// asynchronous, which would mean either a page that starts on the examples and
// swaps them out a moment later or a splash screen held open on a database.
//
// Every call here is guarded: storage throws rather than returning null where
// it's disabled or full — Safari's private mode is the usual one — and a
// playground that can't remember is still a playground.

import type { ExampleFile } from "./example.js";

const KEY = "gqlhash-playground";

/**
 * VERSION is the shape of what's under KEY. Anything else — an older page's
 * write, a newer one's — is dropped rather than guessed at, which costs the
 * files that were open and keeps a shape change from having to be backwards
 * compatible forever.
 */
const VERSION = 1;

/** StoredOptions is the sidebar: what to hash with, and what to leave out. */
export interface StoredOptions {
  readonly hash: string;
  readonly format: string;
  readonly ignore: string;
}

/** StoredPane is one pane: its files, and the name of the active one. */
export interface StoredPane {
  readonly active: string;
  readonly files: readonly ExampleFile[];
}

export interface Stored {
  readonly operations: StoredPane;
  readonly allowlist: StoredPane;
  /**
   * Absent where the options were saved by a page that didn't keep them yet,
   * which is what the reader of an older save gets rather than nothing at all.
   */
  readonly options?: StoredOptions;
}

/** load returns what was saved, or null where there's nothing usable. */
export function load(): Stored | null {
  let raw: string | null;
  try {
    raw = window.localStorage.getItem(KEY);
  } catch {
    return null;
  }
  if (raw === null) {
    return null;
  }

  try {
    const parsed: unknown = JSON.parse(raw);
    if (!isRecord(parsed) || parsed.version !== VERSION) {
      return null;
    }
    const operations = pane(parsed.operations);
    const allowlist = pane(parsed.allowlist);
    if (!operations || !allowlist) {
      return null;
    }
    const saved = options(parsed.options);
    return saved
      ? { operations, allowlist, options: saved }
      : { operations, allowlist };
  } catch {
    return null;
  }
}

/** save writes the state over whatever was there. Callers debounce it. */
export function save(state: Stored): void {
  try {
    window.localStorage.setItem(
      KEY,
      JSON.stringify({ version: VERSION, ...state }),
    );
  } catch {
    // Full, or disabled. Neither is worth interrupting the page over.
  }
}

/** clear drops everything the page has saved in this browser. */
export function clear(): void {
  try {
    window.localStorage.removeItem(KEY);
  } catch {
    // As above.
  }
}

/**
 * pane validates one side of what was read. Nothing here trusts the shape:
 * what's in storage is whatever the last page to run wrote, and an extension,
 * a hand-edited entry or a half-written value would otherwise reach the panes
 * as a file with no text.
 */
function pane(value: unknown): StoredPane | null {
  if (!isRecord(value) || typeof value.active !== "string") {
    return null;
  }
  if (!Array.isArray(value.files) || value.files.length === 0) {
    return null;
  }
  const files: ExampleFile[] = [];
  for (const file of value.files) {
    if (
      !isRecord(file) ||
      typeof file.name !== "string" ||
      typeof file.text !== "string" ||
      file.name === ""
    ) {
      return null;
    }
    files.push({ name: file.name, text: file.text });
  }
  return { active: value.active, files };
}

/**
 * options validates the sidebar's half. Whether the hash function it names is
 * one the module still offers is the page's to check — this only says the
 * shape is what it claims.
 */
function options(value: unknown): StoredOptions | null {
  if (
    !isRecord(value) ||
    typeof value.hash !== "string" ||
    typeof value.format !== "string" ||
    typeof value.ignore !== "string"
  ) {
    return null;
  }
  return { hash: value.hash, format: value.format, ignore: value.ignore };
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null;
}
