// Loads the gqlhash WebAssembly module and wraps the global it installs in a
// typed API. See ../wasm/main.go for the other side of this boundary.

import "./vendor/wasm_exec.js";
import wasmURL from "./wasm/gqlhash.wasm?url";

export interface Option {
  readonly id: string;
  readonly label: string;
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
}

/**
 * load fetches and starts the WebAssembly module. onProgress reports the
 * download in bytes so a splash screen can show it; total is 0 when the server
 * doesn't send a Content-Length.
 */
export async function load(
  onProgress?: (loaded: number, total: number) => void,
): Promise<GQLHash> {
  const response = await fetch(wasmURL);
  if (!response.ok) {
    throw new Error(
      `downloading ${wasmURL}: ${response.status} ${response.statusText}`,
    );
  }

  const bytes = await readWithProgress(response, onProgress);
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

/** readWithProgress buffers the body, reporting how much has arrived. */
async function readWithProgress(
  response: Response,
  onProgress?: (loaded: number, total: number) => void,
): Promise<ArrayBuffer> {
  const total = Number(response.headers.get("Content-Length") ?? 0);
  if (!response.body || !onProgress) {
    return response.arrayBuffer();
  }

  const reader = response.body.getReader();
  const chunks: Uint8Array[] = [];
  let loaded = 0;
  for (;;) {
    const { done, value } = await reader.read();
    if (done) {
      break;
    }
    chunks.push(value);
    loaded += value.byteLength;
    onProgress(loaded, total);
  }

  const buffer = new Uint8Array(loaded);
  let offset = 0;
  for (const chunk of chunks) {
    buffer.set(chunk, offset);
    offset += chunk.byteLength;
  }
  return buffer.buffer;
}
