// Types for the Go toolchain's lib/wasm/wasm_exec.js, which is copied here by
// scripts/build-wasm.sh and installs the `Go` class on globalThis.
declare global {
  class Go {
    argv: string[];
    env: Record<string, string>;
    importObject: WebAssembly.Imports;
    exit: (code: number) => void;
    run(instance: WebAssembly.Instance): Promise<void>;
  }
}

export {};
