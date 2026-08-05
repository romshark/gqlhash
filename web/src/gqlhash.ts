// Loads the gqlhash WebAssembly module and wraps the global it installs in a
// typed API. See ../wasm/main.go for the other side of this boundary.

import "./vendor/wasm_exec.js";
import wasmURL from "./wasm/gqlhash.wasm?url";

export interface Option {
  readonly id: string;
  readonly label: string;
  /**
   * What the hash function is worth to an allowlist, which rests on one
   * property: collision resistance. The value is the label as well — one
   * vocabulary, no table to keep in step. Set on the hash functions, absent
   * on the output formats. See hashFunctionList in ../wasm/main.go.
   */
  readonly kind?: "resistant" | "broken" | "checksum";
}

export interface HashOptions {
  readonly hash: string;
  readonly format: string;
  readonly ignoreInputs: boolean;
  readonly ignoreVariables: boolean;
}

/** ParseError points at where parsing stopped. Line and column are 1-based. */
export interface ParseError {
  readonly message: string;
  readonly offset?: number;
  readonly line?: number;
  readonly column?: number;
}

export type HashResult =
  | { readonly hash: string; readonly bits: number; readonly error?: undefined }
  | { readonly hash?: undefined; readonly error: ParseError };

export interface GQLHash {
  readonly version: string;
  readonly hashFunctions: readonly Option[];
  readonly formats: readonly Option[];
  hash(source: string, options: HashOptions): HashResult;
}

declare global {
  // Installed by the Go program once its main function runs. It has to be var:
  // that's the only declaration that adds a property to globalThis.
  var gqlhash: GQLHash | undefined;
  // The binary, on its way down since the head of the document.
  // See wasmLoader in ../vite.config.ts.
  var gqlhashWasm: Promise<ArrayBuffer> | undefined;
}

/**
 * load starts the WebAssembly module. The loader script in the document has
 * been downloading it since before this bundle arrived, and has been reporting
 * it on the splash screen; all that's left here is to wait for it.
 */
export async function load(): Promise<GQLHash> {
  const bytes = await (globalThis.gqlhashWasm ?? download());
  const go = new Go();
  const { instance } = await WebAssembly.instantiate(bytes, go.importObject);

  // go.run resolves only once the Go program exits. main installs the global
  // and then blocks forever, so the global is set by the time run yields.
  void go.run(instance).catch((error: unknown) => {
    console.error("gqlhash wasm exited:", error);
  });

  const api = globalThis.gqlhash;
  if (!api) {
    throw new Error("gqlhash wasm module did not install its global");
  }
  return api;
}

/** download fetches the binary, for when the loader script didn't run. */
async function download(): Promise<ArrayBuffer> {
  const response = await fetch(wasmURL);
  if (!response.ok) {
    throw new Error(
      `downloading ${wasmURL}: ${response.status} ${response.statusText}`,
    );
  }
  return response.arrayBuffer();
}
