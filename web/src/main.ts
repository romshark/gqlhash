import type { EditorView } from "@codemirror/view";
import { createEditor, setDocument, showError } from "./editor.js";
import { exampleDocuments } from "./example.js";
import {
  type GQLHash,
  type HashOptions,
  load,
  type Option,
} from "./gqlhash.js";

/** Milliseconds to wait after the last keystroke before rehashing. */
const DEBOUNCE_MS = 200;

function element<T extends HTMLElement>(id: string): T {
  const found = document.getElementById(id);
  if (!found) {
    throw new Error(`missing element #${id}`);
  }
  return found as T;
}

const ui = {
  splash: element<HTMLDivElement>("splash"),
  splashStatus: element<HTMLParagraphElement>("splash-status"),
  app: element<HTMLDivElement>("app"),
  version: element<HTMLSpanElement>("version"),
  hashFunction: element<HTMLSelectElement>("hash-function"),
  format: element<HTMLSelectElement>("format"),
  ignoreInputs: element<HTMLInputElement>("ignore-inputs"),
  ignoreVariables: element<HTMLInputElement>("ignore-variables"),
  verdict: element<HTMLElement>("verdict"),
  verdictSign: element<HTMLSpanElement>("verdict-sign"),
  verdictText: element<HTMLSpanElement>("verdict-text"),
  verdictDetail: element<HTMLSpanElement>("verdict-detail"),
};

/** Pane is one document: its editor, its hash and its own error line. */
interface Pane {
  readonly id: "a" | "b";
  readonly label: string;
  readonly example: string;
  readonly hashOutput: HTMLOutputElement;
  readonly status: HTMLSpanElement;
  readonly error: HTMLParagraphElement;
  readonly copy: HTMLButtonElement;
  readonly reset: HTMLButtonElement;
  readonly host: HTMLDivElement;
  view: EditorView;
  /** hash is the last computed hash, null while empty or unparsable. */
  hash: string | null;
  empty: boolean;
  failed: boolean;
}

function pane(id: "a" | "b", label: string, example: string): Pane {
  return {
    id,
    label,
    example,
    hashOutput: element<HTMLOutputElement>(`hash-output-${id}`),
    status: element<HTMLSpanElement>(`status-${id}`),
    error: element<HTMLParagraphElement>(`error-${id}`),
    copy: element<HTMLButtonElement>(`copy-${id}`),
    reset: element<HTMLButtonElement>(`reset-${id}`),
    host: element<HTMLDivElement>(`editor-${id}`),
    // Assigned in start, once the editor exists.
    view: null as unknown as EditorView,
    hash: null,
    empty: true,
    failed: false,
  };
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
  ui.version.textContent = `gqlhash ${shortVersion(api.version)}`;
  ui.version.title = `gqlhash ${api.version}`;
  fillSelect(ui.hashFunction, api.hashFunctions, "sha1");
  fillSelect(ui.format, api.formats, "hex");

  const panes = [
    pane("a", "Document A", exampleDocuments.a),
    pane("b", "Document B", exampleDocuments.b),
  ];

  let timer: number | undefined;
  for (const p of panes) {
    p.view = createEditor({
      parent: p.host,
      doc: p.example,
      onChange: () => {
        // Debounce typing in either editor; option changes rehash immediately.
        window.clearTimeout(timer);
        for (const q of panes) {
          q.status.textContent = "typing…";
          q.status.dataset.state = "pending";
        }
        ui.verdictText.textContent = "Comparing…";
        ui.verdict.dataset.state = "pending";
        timer = window.setTimeout(rehash, DEBOUNCE_MS);
      },
    });
  }

  function currentOptions(): HashOptions {
    return {
      hash: ui.hashFunction.value,
      format: ui.format.value,
      ignoreInputs: ui.ignoreInputs.checked,
      ignoreVariables: ui.ignoreVariables.checked,
    };
  }

  /** rehash recomputes both documents and then the verdict over them. */
  function rehash(): void {
    window.clearTimeout(timer);
    const options = currentOptions();
    for (const p of panes) {
      hashPane(p, api, options);
    }
    showVerdict(panes);
  }

  // ignore-variables implies ignore-inputs, so the ignore-inputs box shows the
  // effective state while it's in force. The user's own setting is remembered
  // and restored, which keeps the implication from quietly changing it.
  let ignoreInputsChoice = ui.ignoreInputs.checked;

  function syncFlags(): void {
    const implied = ui.ignoreVariables.checked;
    ui.ignoreInputs.disabled = implied;
    ui.ignoreInputs.checked = implied || ignoreInputsChoice;
  }

  for (const input of [ui.hashFunction, ui.format]) {
    input.addEventListener("change", rehash);
  }
  ui.ignoreInputs.addEventListener("change", () => {
    ignoreInputsChoice = ui.ignoreInputs.checked;
    rehash();
  });
  ui.ignoreVariables.addEventListener("change", () => {
    syncFlags();
    rehash();
  });

  for (const p of panes) {
    p.reset.addEventListener("click", () => {
      setDocument(p.view, p.example);
      p.view.focus();
      rehash();
    });
    p.copy.addEventListener("click", () => {
      const hash = p.hashOutput.textContent;
      if (!hash) {
        return;
      }
      void navigator.clipboard.writeText(hash).then(
        () => flashCopyButton(p.copy, "Copied"),
        () => flashCopyButton(p.copy, "Copy failed"),
      );
    });
  }

  syncFlags();
  rehash();
}

/** hashPane hashes one document and paints that pane's own result. */
function hashPane(p: Pane, api: GQLHash, options: HashOptions): void {
  const source = p.view.state.doc.toString();

  // An empty editor isn't a mistake worth an error message; the parser would
  // otherwise report an unexpected EOF for a document nobody has written yet.
  if (source.trim() === "") {
    p.hash = null;
    p.empty = true;
    p.failed = false;
    p.hashOutput.textContent = "";
    p.hashOutput.dataset.state = "empty";
    p.error.hidden = true;
    p.status.textContent = "empty";
    p.status.dataset.state = "pending";
    showError(p.view, null);
    return;
  }

  const started = performance.now();
  const result = api.hash(source, options);
  const elapsed = performance.now() - started;

  p.empty = false;

  if (result.error) {
    p.hash = null;
    p.failed = true;
    p.hashOutput.textContent = "";
    p.hashOutput.dataset.state = "error";
    p.error.hidden = false;
    p.error.textContent =
      result.error.line && result.error.column
        ? `${result.error.message} (line ${result.error.line}, column ${result.error.column})`
        : result.error.message;
    p.status.textContent = "syntax error";
    p.status.dataset.state = "error";
    showError(p.view, result.error);
    return;
  }

  p.hash = result.hash;
  p.failed = false;
  p.hashOutput.textContent = result.hash;
  p.hashOutput.dataset.state = "ok";
  p.error.hidden = true;
  p.error.textContent = "";
  p.status.textContent = `${result.bits} bit · ${elapsed.toFixed(2)} ms`;
  p.status.dataset.state = "ok";
  showError(p.view, null);
}

/**
 * showVerdict states whether the two documents hash alike. Both hashes come
 * from the same function and options, so comparing the encoded strings is the
 * same test [gqlhash.Compare] makes on the bytes.
 */
function showVerdict(panes: readonly Pane[]): void {
  const [a, b] = panes;
  if (!a || !b) {
    return;
  }

  const set = (state: string, sign: string, text: string, detail = "") => {
    ui.verdict.dataset.state = state;
    ui.verdictSign.textContent = sign;
    ui.verdictText.textContent = text;
    ui.verdictDetail.textContent = detail;
  };

  const broken = panes.filter((p) => p.failed);
  if (broken.length > 0) {
    const which = broken.map((p) => p.label).join(" and ");
    set(
      "error",
      "!",
      `${which} ${broken.length > 1 ? "don't" : "doesn't"} parse`,
    );
    return;
  }

  const blank = panes.filter((p) => p.empty);
  if (blank.length > 0) {
    const which = blank.map((p) => p.label).join(" and ");
    set("pending", "?", `Waiting for ${which}`);
    return;
  }

  if (a.hash === b.hash) {
    set("match", "≡", "Identical hash", "The documents are equivalent.");
    return;
  }
  set("differ", "≠", "Hashes differ", "The documents are not equivalent.");
}

function flashCopyButton(button: HTMLButtonElement, label: string): void {
  const previous = button.dataset.timer;
  if (previous) {
    window.clearTimeout(Number(previous));
  }
  button.textContent = label;
  button.dataset.timer = String(
    window.setTimeout(() => {
      button.textContent = "Copy";
      delete button.dataset.timer;
    }, 1200),
  );
}

function fillSelect(
  select: HTMLSelectElement,
  options: readonly Option[],
  selected: string,
): void {
  select.replaceChildren(
    ...options.map((option) => {
      const element = document.createElement("option");
      element.value = option.id;
      element.textContent = option.label;
      element.selected = option.id === selected;
      return element;
    }),
  );
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
