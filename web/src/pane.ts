// A pane is one editor with a strip of tabs above it: several files, one of
// them on screen. Both sides of the page are panes — the operations a client
// sends on the left, the allowlist they're checked against on the right — and
// they differ only in what the page writes into each file's status once it has
// hashed them.
//
// The strip is Morpheus' <neo-tabs>, which owns selection, roving focus and
// the arrow keys. This module owns the files: what they hold, what they're
// called, and which of them the shared editor is showing.

import type { EditorState } from "@codemirror/state";
import {
  createEditor,
  createEditorState,
  showError,
  syncTheme,
} from "./editor.js";
import type { ExampleFile } from "./example.js";
import type { ParseError } from "./gqlhash.js";

/**
 * FileState is what a tab's marker says about its file: on the allowlist or
 * allowed by it (ok), refused or skipped (bad), unparsable (error), blank
 * (empty), or not hashed since the last keystroke (pending).
 */
export type FileState = "ok" | "bad" | "error" | "empty" | "pending";

/**
 * Whether the command modifier is ⌘ or Ctrl, resolved the way the kit's own
 * platform.ts resolves it — the structured client hint first, then the two
 * older fields — so the keys that fire agree with the ones <neo-kbd> and
 * <neo-condition> print in the sidebar.
 */
const APPLE = /mac|iphone|ipad|ipod/i.test(
  (navigator as Navigator & { userAgentData?: { platform?: string } })
    .userAgentData?.platform ??
    navigator.platform ??
    navigator.userAgent,
);

/** The icon each state paints on the tab, next to the file name. */
const marks: Record<FileState, string> = {
  ok: "circle-check",
  bad: "circle-x",
  error: "triangle-alert",
  empty: "file",
  pending: "file",
};

export interface PaneFile {
  readonly id: string;
  name: string;
  /** text is the document as it stands, kept current on every keystroke. */
  text: string;
  /** editor is this file's cursor, scroll position and undo history. */
  editor: EditorState;

  // Everything below is written by the page on each rehash and read back when
  // the tabs are painted.
  state: FileState;
  /** note explains state in words, as the tab's tooltip and for a reader. */
  note: string;
  /** matched marks the allowlist entry the operation on screen hashes to. */
  matched: boolean;
  hash: string | null;
  error: ParseError | null;
}

export interface PaneConfig {
  /** kind names the pane and prefixes the ids of the files it holds. */
  readonly kind: "operations" | "allowlist";
  /**
   * tabs is the <neo-tabs> host, tablist the element the tabs themselves go
   * in, and strip the box around it, which also holds the add button: a
   * tablist may own tabs and nothing else.
   */
  readonly tabs: HTMLElement;
  readonly strip: HTMLElement;
  readonly tablist: HTMLElement;
  /** panel is the <neo-tabpanel> the editor lives in. */
  readonly panel: HTMLElement;
  readonly host: HTMLElement;
  /** files is what the pane starts with. */
  readonly files: readonly ExampleFile[];
  /** active names the file of those to open on, if it's still among them. */
  readonly active?: string;
  /** newName names the file the add button creates. */
  readonly newName: (n: number) => string;
  /** addLabel names the add button itself, for a screen reader. */
  readonly addLabel: string;
  /**
   * editorLabel names the editor. It's a text box with no <label> of its own —
   * which file it holds is on the tab above it — so a reader would otherwise
   * reach an unnamed field.
   */
  readonly editorLabel: string;
  /** onEdit fires for every keystroke; the page debounces it. */
  readonly onEdit: () => void;
  /** onUpdate fires once per tab action: switch, add, remove or rename. */
  readonly onUpdate: () => void;
}

export interface Pane {
  readonly files: readonly PaneFile[];
  /** active is the file on screen. A pane always holds at least one. */
  active(): PaneFile;
  /** paint redraws the tabs from what the page wrote into the files. */
  paint(): void;
  /** select puts a file on screen; unknown ids are ignored. */
  select(id: string): void;
  /**
   * restore replaces every file the pane holds, dropping what was in it, and
   * opens the one named by active. It reports nothing: the page asked for it.
   */
  restore(files: readonly ExampleFile[], active?: string): void;
  /** mark points the editor at where the parser stopped, or clears it. */
  mark(error: ParseError | null): void;
  focus(): void;
}

export function createPane(config: PaneConfig): Pane {
  const files: PaneFile[] = [];
  /** tabs holds the element painted for each file, keyed by its id. */
  const tabs = new Map<string, HTMLElement>();
  let activeId = "";
  let created = 0;

  const view = createEditor(config.host, newState(""));
  // Outside the tablist, after it in the strip, so it keeps its place at the
  // end of the tabs without being one of the tablist's children.
  config.strip.append(makeAddButton());

  /** newState builds the state of one file, named for a screen reader. */
  function newState(text: string): EditorState {
    return createEditorState(text, onEdit, config.editorLabel);
  }

  function onEdit(text: string): void {
    const file = byId(activeId);
    if (file) {
      file.text = text;
    }
    config.onEdit();
  }

  function byId(id: string): PaneFile | undefined {
    return files.find((file) => file.id === id);
  }

  function active(): PaneFile {
    const file = byId(activeId) ?? files[0];
    if (!file) {
      throw new Error("pane holds no file");
    }
    return file;
  }

  /**
   * unique keeps two files in a pane from sharing a name. The name is what the
   * verdict points at, so two of them would leave it naming either.
   */
  function unique(name: string, except?: PaneFile): string {
    const taken = (candidate: string) =>
      files.some((file) => file !== except && file.name === candidate);
    if (!taken(name)) {
      return name;
    }
    const dot = name.lastIndexOf(".");
    const stem = dot > 0 ? name.slice(0, dot) : name;
    const extension = dot > 0 ? name.slice(dot) : "";
    for (let n = 2; ; n++) {
      const candidate = `${stem}-${n}${extension}`;
      if (!taken(candidate)) {
        return candidate;
      }
    }
  }

  function makeFile(name: string, text: string): PaneFile {
    created++;
    return {
      id: `${config.kind}-${created}`,
      name: unique(name),
      text,
      editor: newState(text),
      state: "pending",
      note: "",
      matched: false,
      hash: null,
      error: null,
    };
  }

  function select(id: string): void {
    if (id === activeId) {
      return;
    }
    const next = byId(id);
    if (!next) {
      return;
    }
    // The state in the view is the newer one for the file leaving the screen:
    // it's the one that took the edits.
    const current = byId(activeId);
    if (current) {
      current.editor = view.state;
    }
    activeId = id;
    // The panel first: <neo-tabs> hides a panel whose value isn't the selected
    // one, and setting the host's value is what makes it look.
    config.panel.setAttribute("value", id);
    config.tabs.setAttribute("value", id);
    view.setState(next.editor);
    syncTheme(view);
    paint();
  }

  function add(): void {
    const file = makeFile(config.newName(files.length + 1), "");
    files.push(file);
    select(file.id);
    config.onUpdate();
    view.focus();
  }

  /**
   * remove closes a file. The last one stays: a pane with no editor in it has
   * nothing to type into, and an allowlist that allows nothing is a blank file
   * rather than no file.
   */
  function remove(id: string): void {
    if (files.length < 2) {
      return;
    }
    const index = files.findIndex((file) => file.id === id);
    if (index < 0) {
      return;
    }
    files.splice(index, 1);
    const next = files[Math.min(index, files.length - 1)];
    if (id === activeId && next) {
      activeId = "";
      select(next.id);
    } else {
      paint();
    }
    config.onUpdate();
  }

  // A name is whatever it's typed as. The proxy reads .graphql and .gql files
  // out of a directory, but nothing here treats a name as a filename — it's
  // what the verdict points at — and an extension every tab shares tells the
  // tabs apart from each other not at all, at eight characters of strip each.
  function rename(file: PaneFile, name: string): void {
    const wanted = name.trim();
    if (wanted === "") {
      return;
    }
    file.name = unique(wanted, file);
  }

  /**
   * fill replaces the files with from, and opens the one named by active —
   * the first where that names nothing, which is what a fresh set wants and
   * what a saved set whose active file has since been deleted falls back to.
   */
  function fill(from: readonly ExampleFile[], active?: string): void {
    files.length = 0;
    for (const file of from) {
      files.push(makeFile(file.name, file.text));
    }
    activeId = "";
    const open = files.find((file) => file.name === active) ?? files[0];
    if (open) {
      select(open.id);
    }
  }

  /* --- Tabs -------------------------------------------------------------- */

  /**
   * paint brings the strip in line with the files. Tabs are updated in place
   * and only rebuilt when the set or the order of the files has changed: this
   * runs on every rehash, and replacing the strip under a tab that has the
   * focus would drop it.
   */
  function paint(): void {
    const wanted: HTMLElement[] = [];
    for (const file of files) {
      let tab = tabs.get(file.id);
      if (!tab) {
        tab = makeTab(file);
        tabs.set(file.id, tab);
      }
      updateTab(tab, file);
      wanted.push(tab);
    }

    for (const [id, tab] of tabs) {
      if (!byId(id)) {
        tab.remove();
        tabs.delete(id);
      }
    }

    const current = Array.from(config.tablist.children);
    const same =
      current.length === wanted.length &&
      wanted.every((tab, index) => current[index] === tab);
    if (!same) {
      config.tablist.replaceChildren(...wanted);
    }
  }

  function makeTab(file: PaneFile): HTMLElement {
    const tab = document.createElement("neo-tab");
    tab.setAttribute("value", file.id);

    const mark = document.createElement("neo-icon");
    mark.className = "tab-mark";

    const name = document.createElement("span");
    name.className = "tab-name";

    // The state is on the tab as an icon and a color, neither of which a
    // screen reader announces, so it's also in the tab's accessible name.
    const status = document.createElement("span");
    status.className = "visually-hidden";

    const badge = document.createElement("neo-badge");
    badge.className = "tab-badge";
    badge.setAttribute("variant", "outline");
    badge.textContent = "matched";

    // Out of the tab order on purpose: a tablist moves between tabs with the
    // arrow keys, and a focus stop inside every tab would break that. Delete
    // removes the file from the keyboard.
    const close = document.createElement("button");
    close.className = "tab-close";
    close.type = "button";
    close.tabIndex = -1;
    close.textContent = "×";
    close.addEventListener("click", (event) => {
      // Otherwise <neo-tabs> reads the click as "select this tab" as well.
      event.stopPropagation();
      remove(file.id);
    });

    tab.append(mark, name, status, badge, close);
    tab.addEventListener("dblclick", () => beginRename(file, tab));
    return tab;
  }

  function updateTab(tab: HTMLElement, file: PaneFile): void {
    tab.dataset.state = file.state;
    if (file.matched) {
      tab.dataset.matched = "true";
    } else {
      delete tab.dataset.matched;
    }
    tab.title = file.note ? `${file.name} — ${file.note}` : file.name;

    tab.querySelector(".tab-mark")?.setAttribute("name", marks[file.state]);
    const name = tab.querySelector(".tab-name");
    if (name) {
      name.textContent = file.name;
    }
    const status = tab.querySelector(".visually-hidden");
    if (status) {
      status.textContent = file.note ? `, ${file.note}` : "";
    }
    const badge = tab.querySelector<HTMLElement>(".tab-badge");
    if (badge) {
      badge.hidden = !file.matched;
    }
    const close = tab.querySelector<HTMLButtonElement>(".tab-close");
    if (close) {
      close.hidden = files.length < 2;
      close.setAttribute("aria-label", `Delete ${file.name}`);
      close.title = `Delete ${file.name}`;
    }
  }

  function makeAddButton(): HTMLElement {
    const button = document.createElement("neo-button");
    button.className = "tab-add";
    button.setAttribute("variant", "ghost");
    button.setAttribute("size", "sm");
    button.setAttribute("aria-label", config.addLabel);
    button.title = config.addLabel;
    const icon = document.createElement("neo-icon");
    icon.setAttribute("name", "plus");
    button.append(icon);
    button.addEventListener("click", add);
    return button;
  }

  /** beginRename turns the tab into a text field until it's committed. */
  function beginRename(file: PaneFile, tab: HTMLElement): void {
    const input = document.createElement("input");
    input.className = "tab-rename";
    input.value = file.name;
    input.setAttribute("aria-label", `Rename ${file.name}`);

    let settled = false;
    const settle = (save: boolean) => {
      if (settled) {
        return;
      }
      settled = true;
      if (save) {
        rename(file, input.value);
      }
      // The tab's own children were replaced by the field, so it's built
      // again rather than updated in place.
      tabs.delete(file.id);
      tab.remove();
      paint();
      config.onUpdate();
      focusActiveTab();
    };

    input.addEventListener("keydown", (event) => {
      // The tablist's own keys would otherwise fire while the caret is in the
      // name being typed.
      event.stopPropagation();
      if (event.key === "Enter") {
        settle(true);
      } else if (event.key === "Escape") {
        settle(false);
      }
    });
    input.addEventListener("blur", () => settle(true));

    tab.replaceChildren(input);
    input.focus();
    input.select();
  }

  function focusActiveTab(): void {
    tabs.get(activeId)?.focus();
  }

  // Selection is the component's: a click or an arrow key commits it and says
  // so, and this follows with the editor.
  config.tabs.addEventListener("neo-tabs-change", (event) => {
    select(event.detail.value);
    config.onUpdate();
  });

  // Renaming and deleting are ours. Both are guarded on the focus being on a
  // tab, the way <neo-tabs> guards its own keys — the editor is inside the
  // component too, and its keystrokes bubble through here.
  config.tablist.addEventListener("keydown", (event) => {
    const tab = (event.target as Element | null)?.closest("neo-tab");
    if (!tab) {
      return;
    }
    const file = byId(tab.getAttribute("value") ?? "");
    if (!file) {
      return;
    }
    if (event.key === "F2") {
      event.preventDefault();
      beginRename(file, tab as HTMLElement);
      return;
    }

    // Delete where a keyboard has one, and the command modifier with
    // Backspace where the delete key is Backspace — ⌘⌫ is what a Mac deletes
    // an item from a list with, and a bare ⌫ there is too easy to lean on.
    const deleting =
      event.key === "Delete" ||
      (event.key === "Backspace" && (APPLE ? event.metaKey : event.ctrlKey));
    if (deleting) {
      event.preventDefault();
      remove(file.id);
      focusActiveTab();
    }
  });

  fill(config.files, config.active);

  return {
    files,
    active,
    paint,
    select,
    restore: fill,
    mark: (error) => showError(view, error),
    focus: () => view.focus(),
  };
}
