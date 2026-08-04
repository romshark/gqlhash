import { exampleAllowlist, exampleOperations } from "./example.js";
import {
  type GQLHash,
  type HashOptions,
  load,
  type Option,
} from "./gqlhash.js";
import { createPane, type Pane, type PaneFile } from "./pane.js";
import * as storage from "./storage.js";
import * as theme from "./theme.js";
// Defines the Morpheus custom elements the page is built from: the sidebar,
// the tab strips, the selects, the checkboxes, the verdict's alert.
import "./vendor/morpheus/bundle.js";

/**
 * Options is the sidebar. It carries -ignore the way the flag does — one of
 * three levels — where the hasher takes the two booleans that fall out of it.
 */
interface Options {
  readonly hash: string;
  readonly format: string;
  readonly ignore: string;
}

/**
 * What the sidebar opens on where nothing was saved, and what the reset puts
 * back. sha2 is what gqlhash-proxy runs on, and this page is that proxy's
 * decision.
 */
const DEFAULT_OPTIONS: Options = {
  hash: "sha2",
  format: "hex",
  // Wider than the proxy's own default, which is nothing at all: it's what
  // the first pair of files opens on, where the sent document writes its id
  // in and the registered one takes a variable.
  ignore: "variables",
};

/** Milliseconds to wait after the last keystroke before rehashing. */
const DEBOUNCE_MS = 200;

/**
 * Milliseconds to wait before writing the files to the browser. Longer than
 * the rehash: what's on screen has to keep up with the typing, where the copy
 * in storage only has to be there for the next visit.
 */
const SAVE_MS = 300;

function element<T extends HTMLElement>(id: string): T {
  const found = document.getElementById(id);
  if (!found) {
    throw new Error(`missing element #${id}`);
  }
  return found as T;
}

/**
 * panelOf returns the panel of a <neo-tabs>. It isn't looked up by id the way
 * everything else is: the component derives the id of a panel from the value
 * it carries and rewrites it on every switch, so an id of ours wouldn't
 * survive the first one.
 */
function panelOf(tabs: HTMLElement): HTMLElement {
  const panel = tabs.querySelector<HTMLElement>(":scope > neo-tabpanel");
  if (!panel) {
    throw new Error(`#${tabs.id} holds no panel`);
  }
  return panel;
}

/** stripOf returns the box a pane's tabs and its add button share. */
function stripOf(tabs: HTMLElement): HTMLElement {
  const strip = tabs.querySelector<HTMLElement>(":scope > .tab-strip");
  if (!strip) {
    throw new Error(`#${tabs.id} holds no tab strip`);
  }
  return strip;
}

const ui = {
  splash: element<HTMLDivElement>("splash"),
  splashStatus: element<HTMLParagraphElement>("splash-status"),
  app: element<HTMLDivElement>("app"),
  sidebar: element("sidebar"),
  sidebarToggle: element("sidebar-toggle"),
  version: element<HTMLSpanElement>("version"),
  hashFunction: element("hash-function"),
  format: element("format"),
  ignore: element("ignore"),
  verdict: element("verdict"),
  verdictIcon: element("verdict-icon"),
  verdictText: element<HTMLHeadingElement>("verdict-text"),
  resetConfirm: element("reset-confirm"),
};

/**
 * Side is one half of the page: a pane of files and the readout under it, which
 * reports the hash of whichever of them is on screen.
 */
interface Side {
  readonly pane: Pane;
  readonly hashOutput: HTMLOutputElement;
  readonly error: HTMLParagraphElement;
  readonly summary: HTMLSpanElement;
}

void main();

async function main(): Promise<void> {
  let api: GQLHash;
  try {
    api = await load((loaded, total) => {
      ui.splashStatus.textContent = total
        ? `Loading WebAssembly… ${Math.round((loaded / total) * 100)}%`
        : `Loading WebAssembly… ${formatBytes(loaded)}`;
    });
  } catch (error) {
    ui.splash.classList.add("splash-failed");
    ui.splashStatus.textContent = `Failed to load: ${
      error instanceof Error ? error.message : String(error)
    }`;
    return;
  }

  ui.splashStatus.textContent = "Starting editors…";
  start(api);

  ui.app.hidden = false;
  ui.splash.classList.add("splash-done");
  ui.splash.addEventListener("transitionend", () => ui.splash.remove(), {
    once: true,
  });
}

function start(api: GQLHash): void {
  // One switch in the sidebar's header, one in the toolbar the sidebar is
  // opened from when it's an overlay; whichever is on screen, both follow.
  theme.mount(element("theme-sidebar"));
  theme.mount(element("theme-navbar"));

  ui.version.textContent = `gqlhash ${shortVersion(api.version)}`;
  ui.version.title = `gqlhash ${api.version}`;

  // What this browser had last time, if it still holds anything usable.
  const saved = storage.load();

  /**
   * offered keeps a saved id that the module no longer has — an option
   * renamed or dropped since — from reaching the hasher, which would answer
   * "unsupported hash function" for every file until the select was touched.
   */
  const offered = (options: readonly Option[], id: string, fallback: string) =>
    options.some((option) => option.id === id) ? id : fallback;

  const opening = saved?.options ?? DEFAULT_OPTIONS;
  fillSelect(
    ui.hashFunction,
    api.hashFunctions,
    offered(api.hashFunctions, opening.hash, DEFAULT_OPTIONS.hash),
  );
  fillSelect(
    ui.format,
    api.formats,
    offered(api.formats, opening.format, DEFAULT_OPTIONS.format),
  );
  // The page's own truth for the -ignore level; the group's attribute mirrors
  // it. Reading the attribute back wouldn't do: pressing the pressed toggle
  // clears it, and "no level" isn't one of the three.
  let ignore = opening.ignore;
  ui.ignore.setAttribute("value", ignore);

  let timer: number | undefined;
  let saveTimer: number | undefined;

  /** edited debounces the rehash and says so until it has run. */
  function edited(side: Side): void {
    window.clearTimeout(timer);
    const file = side.pane.active();
    file.state = "pending";
    file.note = "";
    side.pane.paint();
    setVerdict("info", "info", "Checking…");
    timer = window.setTimeout(rehash, DEBOUNCE_MS);
    saveSoon();
  }

  /** saveSoon writes both panes to the browser once the typing stops. */
  function saveSoon(): void {
    window.clearTimeout(saveTimer);
    saveTimer = window.setTimeout(() => {
      storage.save({
        operations: snapshot(operations.pane),
        allowlist: snapshot(allowlist.pane),
        options: currentOptions(),
      });
    }, SAVE_MS);
  }

  const operationTabs = element("tabs-operations");
  const allowlistTabs = element("tabs-allowlist");

  const operations: Side = {
    pane: createPane({
      kind: "operations",
      tabs: operationTabs,
      strip: stripOf(operationTabs),
      tablist: element("tablist-operations"),
      panel: panelOf(operationTabs),
      host: element("editor-operations"),
      files: saved?.operations.files ?? exampleOperations,
      active: saved?.operations.active,
      newName: (n) => `operation-${n}`,
      addLabel: "New operation file",
      editorLabel: "Operation",
      onEdit: () => edited(operations),
      onUpdate: () => {
        rehash();
        saveSoon();
      },
    }),
    hashOutput: element<HTMLOutputElement>("hash-operations"),
    error: element<HTMLParagraphElement>("error-operations"),
    summary: element<HTMLSpanElement>("summary-operations"),
  };

  const allowlist: Side = {
    pane: createPane({
      kind: "allowlist",
      tabs: allowlistTabs,
      strip: stripOf(allowlistTabs),
      tablist: element("tablist-allowlist"),
      panel: panelOf(allowlistTabs),
      host: element("editor-allowlist"),
      files: saved?.allowlist.files ?? exampleAllowlist,
      active: saved?.allowlist.active,
      newName: (n) => `entry-${n}`,
      addLabel: "New allowlist entry",
      editorLabel: "Allowlist entry",
      onEdit: () => edited(allowlist),
      onUpdate: () => {
        rehash();
        saveSoon();
      },
    }),
    hashOutput: element<HTMLOutputElement>("hash-allowlist"),
    error: element<HTMLParagraphElement>("error-allowlist"),
    summary: element<HTMLSpanElement>("summary-allowlist"),
  };

  function currentOptions(): Options {
    return {
      hash: ui.hashFunction.getAttribute("value") ?? DEFAULT_OPTIONS.hash,
      format: ui.format.getAttribute("value") ?? DEFAULT_OPTIONS.format,
      ignore,
    };
  }

  /**
   * rehash hashes every file on both sides, looks each operation up in the
   * allowlist and repaints everything that reports the outcome.
   */
  function rehash(): void {
    window.clearTimeout(timer);
    const options = hashOptions(currentOptions());
    for (const file of allowlist.pane.files) {
      hashFile(file, api, options);
    }
    for (const file of operations.pane.files) {
      hashFile(file, api, options);
    }

    const entries = indexAllowlist(allowlist.pane.files);
    const active = operations.pane.active();
    for (const file of operations.pane.files) {
      judge(file, entries);
    }

    // Only the operation on screen marks an entry: the tabs opposite answer
    // "what does this one match", not "what does anything match".
    for (const file of allowlist.pane.files) {
      file.matched = false;
    }
    const matched = matchOf(active, entries);
    if (matched) {
      matched.matched = true;
      matched.note = `Allowlist entry, matched by ${active.name}`;
    }

    showVerdict(active, matched);
    for (const side of [operations, allowlist]) {
      side.pane.paint();
      showResult(side);
    }
    operations.summary.textContent = countOperations(operations.pane.files);
    allowlist.summary.textContent = countEntries(allowlist.pane.files);
    operations.pane.mark(active.error);
    allowlist.pane.mark(allowlist.pane.active().error);
  }

  for (const select of [ui.hashFunction, ui.format]) {
    select.addEventListener("neo-select-change", () => {
      rehash();
      saveSoon();
    });
  }

  // <neo-toggle-group> holds a list of values rather than a single one, so
  // what a click leaves behind is trimmed back to the one that was pressed.
  // Pressing the pressed one leaves nothing behind and puts the level back.
  ui.ignore.addEventListener("neo-toggle-group-change", (event) => {
    ignore = event.detail.values.find((value) => value !== ignore) ?? ignore;
    ui.ignore.setAttribute("value", ignore);
    rehash();
    saveSoon();
  });

  // The dialog closes itself; this is what its confirming button leaves to do.
  // The pending save is dropped rather than allowed to write the files back
  // over what was just cleared, and nothing is written in its place: no entry
  // is what a fresh browser has, and the examples are what that opens on.
  ui.resetConfirm.addEventListener("click", () => {
    window.clearTimeout(saveTimer);
    storage.clear();
    theme.clear();
    ui.hashFunction.setAttribute("value", DEFAULT_OPTIONS.hash);
    ui.format.setAttribute("value", DEFAULT_OPTIONS.format);
    ignore = DEFAULT_OPTIONS.ignore;
    ui.ignore.setAttribute("value", ignore);
    operations.pane.restore(exampleOperations);
    allowlist.pane.restore(exampleAllowlist);
    rehash();
  });

  // Only reachable while the sidebar is an overlay; the button that closes it
  // again carries data-neo-sidebar-close, which the component handles itself.
  ui.sidebarToggle.addEventListener("click", () => {
    ui.sidebar.setAttribute("open", "true");
  });

  rehash();
}

/**
 * hashOptions turns the sidebar's -ignore level into the pair the hasher
 * takes. The wider level carries the narrower one, the way the flag does.
 */
function hashOptions(options: Options): HashOptions {
  return {
    hash: options.hash,
    format: options.format,
    ignoreInputs: options.ignore === "inputs" || options.ignore === "variables",
    ignoreVariables: options.ignore === "variables",
  };
}

/** hashFile hashes one file and records what came back on the file itself. */
function hashFile(file: PaneFile, api: GQLHash, options: HashOptions): void {
  file.hash = null;
  file.error = null;

  // An empty editor isn't a mistake worth an error message; the parser would
  // otherwise report an unexpected EOF for a document nobody has written yet.
  if (file.text.trim() === "") {
    file.state = "empty";
    return;
  }

  const result = api.hash(file.text, options);
  if (result.error) {
    file.state = "error";
    file.error = result.error;
    return;
  }

  file.state = "ok";
  file.hash = result.hash;
}

/**
 * indexAllowlist gathers the entries by hash and writes each file's standing
 * into it. Two files that hash alike are both skipped, as the proxy skips
 * them: which one a request meant is unknowable, so neither is served. The
 * files are left in the index all the same, so an operation matching them can
 * be told why it isn't allowed.
 */
function indexAllowlist(files: readonly PaneFile[]): Map<string, PaneFile[]> {
  const entries = new Map<string, PaneFile[]>();
  for (const file of files) {
    if (file.hash === null) {
      continue;
    }
    const same = entries.get(file.hash);
    if (same) {
      same.push(file);
    } else {
      entries.set(file.hash, [file]);
    }
  }

  for (const file of files) {
    switch (file.state) {
      case "empty":
        file.note = "Blank, not an entry";
        break;
      case "error":
        file.note = "Doesn't parse, skipped";
        break;
      default: {
        const same = file.hash === null ? [] : (entries.get(file.hash) ?? []);
        if (same.length > 1) {
          const others = same
            .filter((other) => other !== file)
            .map((other) => other.name)
            .join(", ");
          file.state = "bad";
          file.note = `Same hash as ${others}, so neither is used`;
        } else {
          file.state = "ok";
          file.note = "Allowlist entry";
        }
      }
    }
  }
  return entries;
}

/** matchOf returns the entry an operation is served by, if there is one. */
function matchOf(
  operation: PaneFile,
  entries: Map<string, PaneFile[]>,
): PaneFile | null {
  if (operation.hash === null) {
    return null;
  }
  const same = entries.get(operation.hash);
  // More than one file under the hash means all of them are skipped.
  return same && same.length === 1 ? (same[0] ?? null) : null;
}

/** judge writes what the allowlist makes of one operation into its tab. */
function judge(operation: PaneFile, entries: Map<string, PaneFile[]>): void {
  switch (operation.state) {
    case "empty":
      operation.note = "Blank, nothing to check";
      return;
    case "error":
      operation.note = "Rejected, doesn't parse";
      return;
    default:
      break;
  }

  const matched = matchOf(operation, entries);
  if (matched) {
    operation.state = "ok";
    operation.note = `Allowed by ${matched.name}`;
    return;
  }

  operation.state = "bad";
  const same =
    operation.hash === null ? undefined : entries.get(operation.hash);
  operation.note = same
    ? "Rejected, the entries it matches have the same hash"
    : "Rejected, no entry has this hash";
}

/**
 * showVerdict states what the proxy would answer for the operation on screen.
 *
 * Every refusal here is the same answer: 403. A document that doesn't parse
 * isn't on the allowlist either — see proxy.allow in
 * internal/app/proxy/proxy.go, which reports it as not allowed rather than as
 * an error — and neither is one whose entries were dropped for sharing a hash,
 * nor anything at all where the allowlist is empty. Which of those it was is
 * on the tab that carries it and in the readout under the editor; the banner
 * is the answer.
 */
function showVerdict(operation: PaneFile, matched: PaneFile | null): void {
  if (operation.state === "empty") {
    setVerdict("info", "info", "Nothing to check");
    return;
  }

  if (matched) {
    setVerdict("success", "circle-check", "Allowed — 200 OK");
    return;
  }

  if (operation.state === "error") {
    setVerdict("danger", "triangle-alert", "Rejected — 403 Forbidden");
    return;
  }

  setVerdict("warning", "circle-x", "Rejected — 403 Forbidden");
}

/** setVerdict paints the banner's tone, icon and line of text. */
function setVerdict(variant: string, icon: string, text: string): void {
  ui.verdict.setAttribute("variant", variant);
  ui.verdictIcon.setAttribute("name", icon);
  ui.verdictText.textContent = text;
}

/** showResult paints the readout for the file a side has on screen. */
function showResult(side: Side): void {
  const file = side.pane.active();

  side.hashOutput.textContent = file.hash ?? "";
  side.hashOutput.dataset.state =
    file.hash !== null ? "ok" : file.state === "error" ? "error" : "empty";

  side.error.hidden = file.error === null;
  side.error.textContent = file.error
    ? file.error.line && file.error.column
      ? `${file.error.message} (line ${file.error.line}, column ${file.error.column})`
      : file.error.message
    : "";
}

/** snapshot is what a pane holds, in the shape storage keeps it in. */
function snapshot(pane: Pane): storage.StoredPane {
  return {
    active: pane.active().name,
    files: pane.files.map((file) => ({ name: file.name, text: file.text })),
  };
}

function countOperations(files: readonly PaneFile[]): string {
  const allowed = files.filter((file) => file.state === "ok").length;
  return `${allowed} of ${files.length} allowed`;
}

function countEntries(files: readonly PaneFile[]): string {
  const served = files.filter((file) => file.state === "ok").length;
  const skipped = files.length - served;
  const entries = `${served} ${served === 1 ? "entry" : "entries"}`;
  return skipped > 0 ? `${entries} · ${skipped} skipped` : entries;
}

/**
 * The kinds in one letter, for the trigger — where the tag has to share a line
 * with the name and the caret. Collision resistance is what you'd hope for, so
 * it gets no letter: the ones worth a mark are the two that can't carry an
 * allowlist.
 */
const kindLetters: Record<string, string> = {
  broken: "B",
  checksum: "C",
};

/**
 * fillSelect fills a <neo-select> with the options the module reports. A hash
 * function carries what it's worth to an allowlist: spelled out in the list,
 * and — for the two kinds that can't carry one — as a single letter on the
 * trigger, through the compact face the kit clones from
 * [data-neo-option-trigger]. The mark is worth having where the choice already
 * stands, not only in the list you pick it from; spelled out there it would
 * cost the name the room to be read.
 */
function fillSelect(
  select: HTMLElement,
  options: readonly Option[],
  selected: string,
): void {
  select.replaceChildren(
    ...options.map((option) => {
      const element = document.createElement("neo-option");
      element.setAttribute("value", option.id);
      element.append(name(option.label));

      if (option.kind) {
        element.append(badge(option.kind));

        const face = document.createElement("span");
        face.dataset.neoOptionTrigger = "";
        face.append(name(option.label));
        const letter = kindLetters[option.kind];
        if (letter) {
          face.append(letterBadge(option.kind, letter));
        }
        element.append(face);
      }
      return element;
    }),
  );
  select.setAttribute("value", selected);
}

function name(label: string): HTMLElement {
  const element = document.createElement("span");
  element.className = "option-name";
  element.textContent = label;
  return element;
}

function badge(kind: string): HTMLElement {
  const element = document.createElement("neo-badge");
  element.className = "kind-tag";
  element.dataset.kind = kind;
  element.textContent = kind;
  return element;
}

/**
 * letterBadge is the badge above in one character. The word it stands for is
 * its tooltip, and is read out with it — a lone "B" says nothing on its own.
 */
function letterBadge(kind: string, letter: string): HTMLElement {
  const element = badge(kind);
  element.classList.add("kind-letter");
  element.textContent = letter;
  element.title = kind;

  const spelled = document.createElement("span");
  spelled.className = "visually-hidden";
  spelled.textContent = ` ${kind}`;
  element.append(spelled);
  return element;
}

function formatBytes(bytes: number): string {
  return `${(bytes / 1024 / 1024).toFixed(1)} MB`;
}

/**
 * shortVersion trims a Go pseudo-version down to the tag and the short commit
 * hash, e.g. v1.2.6-0.20260725234538-bd527a89e550 to v1.2.6-bd527a89. Released
 * versions are returned unchanged. The footer keeps the full string as its
 * title attribute.
 */
function shortVersion(version: string): string {
  const parts = /^(v[\d.]+)-\d+\.\d{14}-([0-9a-f]{8})[0-9a-f]*(\+dirty)?$/.exec(
    version,
  );
  return parts ? `${parts[1]}-${parts[2]}${parts[3] ?? ""}` : version;
}
