import type { EditorView } from "@codemirror/view";
import { createEditor, setDocument, showError } from "./editor.js";
import { exampleDocument } from "./example.js";
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
  hashOutput: element<HTMLOutputElement>("hash-output"),
  status: element<HTMLSpanElement>("status"),
  error: element<HTMLParagraphElement>("error"),
  copy: element<HTMLButtonElement>("copy"),
  reset: element<HTMLButtonElement>("reset"),
  hashFunction: element<HTMLSelectElement>("hash-function"),
  format: element<HTMLSelectElement>("format"),
  ignoreInputs: element<HTMLInputElement>("ignore-inputs"),
  ignoreVariables: element<HTMLInputElement>("ignore-variables"),
  editorHost: element<HTMLDivElement>("editor"),
};

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

  ui.splashStatus.textContent = "Starting editor…";
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

  let timer: number | undefined;
  const view: EditorView = createEditor({
    parent: ui.editorHost,
    doc: exampleDocument,
    onChange: () => {
      // Debounce typing; option changes below rehash immediately.
      window.clearTimeout(timer);
      ui.status.textContent = "typing…";
      ui.status.dataset.state = "pending";
      timer = window.setTimeout(rehash, DEBOUNCE_MS);
    },
  });

  function rehash(): void {
    window.clearTimeout(timer);
    const options: HashOptions = {
      hash: ui.hashFunction.value,
      format: ui.format.value,
      ignoreInputs: ui.ignoreInputs.checked,
      ignoreVariables: ui.ignoreVariables.checked,
    };

    const started = performance.now();
    const result = api.hash(view.state.doc.toString(), options);
    const elapsed = performance.now() - started;

    if (result.error) {
      ui.hashOutput.textContent = "";
      ui.hashOutput.dataset.state = "error";
      ui.error.hidden = false;
      ui.error.textContent =
        result.error.line && result.error.column
          ? `${result.error.message} (line ${result.error.line}, column ${result.error.column})`
          : result.error.message;
      ui.status.textContent = "syntax error";
      ui.status.dataset.state = "error";
      showError(view, result.error);
      return;
    }

    ui.hashOutput.textContent = result.hash;
    ui.hashOutput.dataset.state = "ok";
    ui.error.hidden = true;
    ui.error.textContent = "";
    ui.status.textContent = `${result.bits} bit · ${elapsed.toFixed(2)} ms`;
    ui.status.dataset.state = "ok";
    showError(view, null);
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

  ui.reset.addEventListener("click", () => {
    setDocument(view, exampleDocument);
    view.focus();
    rehash();
  });

  ui.copy.addEventListener("click", () => {
    const hash = ui.hashOutput.textContent;
    if (!hash) {
      return;
    }
    void navigator.clipboard.writeText(hash).then(
      () => flashCopyButton("Copied"),
      () => flashCopyButton("Copy failed"),
    );
  });

  syncFlags();
  rehash();
}

let copyTimer: number | undefined;

function flashCopyButton(label: string): void {
  ui.copy.textContent = label;
  window.clearTimeout(copyTimer);
  copyTimer = window.setTimeout(() => {
    ui.copy.textContent = "Copy";
  }, 1200);
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
