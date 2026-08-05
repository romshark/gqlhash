import { spawn } from "node:child_process";
import { createHash } from "node:crypto";
import {
  createReadStream,
  existsSync,
  readdirSync,
  readFileSync,
  statSync,
  writeFileSync,
} from "node:fs";
import { join, resolve, sep } from "node:path";
import { createGzip } from "node:zlib";
import { defineConfig, type Plugin } from "vite";

/**
 * goWasmWatch rebuilds src/wasm/gqlhash.wasm when a Go source changes.
 * Vite watches the binary itself and reloads the page.
 */
function goWasmWatch(): Plugin {
  // Everything the binary is compiled from.
  const sources = ["./wasm/*.go", "../*.go", "../parser/*.go"];

  return {
    name: "go-wasm-watch",
    apply: "serve",

    configureServer(server) {
      server.watcher.add(sources);

      let building = false;
      let again = false;

      function build(): void {
        if (building) {
          again = true;
          return;
        }
        building = true;

        server.config.logger.info("go-wasm-watch: rebuilding gqlhash.wasm");
        const child = spawn("./scripts/build-wasm.sh", {
          cwd: server.config.root,
          stdio: ["ignore", "ignore", "pipe"],
        });

        let stderr = "";
        child.stderr.on("data", (chunk: Buffer) => {
          stderr += chunk.toString();
        });

        child.on("close", (code) => {
          building = false;
          if (code === 0) {
            server.config.logger.info("go-wasm-watch: rebuilt gqlhash.wasm");
          } else {
            server.config.logger.error(
              `go-wasm-watch: build failed\n${stderr.trim()}`,
            );
          }
          if (again) {
            again = false;
            build();
          }
        });
      }

      let timer: NodeJS.Timeout | undefined;
      for (const event of ["add", "change", "unlink"] as const) {
        server.watcher.on(event, (path) => {
          if (!path.endsWith(".go")) {
            return;
          }
          // Editors save in bursts; one rebuild per burst is enough.
          clearTimeout(timer);
          timer = setTimeout(build, 100);
        });
      }
    },
  };
}

/**
 * wasmLoader puts the script that downloads the WebAssembly binary in the head.
 * It runs before the entry chunk arrives. Downloading from the bundle instead
 * leaves the splash screen with nothing to count for as long as the bundle
 * takes, which on a slow connection is most of the wait.
 * gqlhash.ts waits on the promise it leaves behind.
 *
 * Neither value can be hand-written into index.html: the build hashes the name,
 * and the percentage needs the unpacked size, not the gzipped Content-Length.
 */
function wasmLoader(): Plugin {
  // Where the binary is served from and its unpacked size, both known only
  // once the bundle is written.
  let href = "";
  let size = 0;
  let root = "";

  return {
    name: "wasm-loader",

    configResolved(config) {
      root = config.root;
    },

    // In dev the file is served unhashed from src/.
    configureServer() {
      href = "/src/wasm/gqlhash.wasm";
    },

    generateBundle(_options, bundle) {
      for (const [name, output] of Object.entries(bundle)) {
        if (output.type === "asset" && name.endsWith(".wasm")) {
          href = `./${name}`;
          size = output.source.length;
        }
      }
    },

    transformIndexHtml: {
      // After the bundle, when the hashed name is known.
      order: "post",
      handler() {
        if (!href) {
          return [];
        }
        // Measured here rather than at startup: in dev the watcher rebuilds it.
        const total = size || fileSize(join(root, "src/wasm/gqlhash.wasm"));
        return [
          {
            tag: "script",
            injectTo: "head",
            children: loaderScript(href, total),
          },
        ];
      },
    },
  };
}

/** fileSize returns the size of a file in bytes, or 0 if it can't be read. */
function fileSize(file: string): number {
  try {
    return statSync(file).size;
  } catch {
    return 0;
  }
}

/**
 * loaderScript downloads the binary and counts it out on the splash screen.
 * The status line is looked up each time: the head runs before it exists.
 */
function loaderScript(href: string, total: number): string {
  return `
(() => {
  const url = ${JSON.stringify(href)};
  const total = ${total};

  const show = (loaded) => {
    const status = document.getElementById("splash-status");
    if (status) {
      status.textContent = total
        ? "Loading WebAssembly… " + Math.round((loaded / total) * 100) + "%"
        : "Loading WebAssembly… " + (loaded / 1048576).toFixed(1) + " MB";
    }
  };

  window.gqlhashWasm = (async () => {
    const response = await fetch(url);
    if (!response.ok) {
      throw new Error(
        "downloading " + url + ": " + response.status + " " + response.statusText,
      );
    }
    if (!response.body) {
      return response.arrayBuffer();
    }

    const reader = response.body.getReader();
    const chunks = [];
    let loaded = 0;
    show(0);
    for (;;) {
      const { done, value } = await reader.read();
      if (done) {
        break;
      }
      chunks.push(value);
      loaded += value.byteLength;
      show(loaded);
    }

    const bytes = new Uint8Array(loaded);
    let offset = 0;
    for (const chunk of chunks) {
      bytes.set(chunk, offset);
      offset += chunk.byteLength;
    }
    return bytes.buffer;
  })();

  // Nothing awaits this until the bundle runs, too late to count as handled.
  // It still rejects there.
  window.gqlhashWasm.catch(() => {});
})();
`;
}

/**
 * serviceWorker writes dist/sw.js: src/sw.js with the list of files this build
 * produced, and a version derived from what's in them.
 *
 * The list can't be written by hand. Half of it is content-hashed,
 * and the other half is whatever public/ happens to hold.
 * The version has to follow the contents rather than the names: index.html and
 * the icons keep their names from build to build.
 */
function serviceWorker(): Plugin {
  let root = "";
  let outDir = "";

  return {
    name: "service-worker",
    apply: "build",

    configResolved(config) {
      root = config.root;
      outDir = resolve(config.root, config.build.outDir);
    },

    // Last, when public/ has been copied and there is a whole site to list.
    closeBundle() {
      const files = builtFiles(outDir).filter((file) => file !== "sw.js");
      const hash = createHash("sha256");
      for (const file of files) {
        hash.update(file);
        hash.update(readFileSync(join(outDir, file)));
      }

      const binary = files.find((file) => file.endsWith(".wasm")) ?? "";
      const shell = files.filter((file) => file !== binary);
      const source = readFileSync(join(root, "src/sw.js"), "utf8")
        .replace("__VERSION__", hash.digest("hex").slice(0, 16))
        // "./" is the start URL, which is what a visitor to the site asks for.
        .replace('["__SHELL__"]', JSON.stringify(["./", ...shell]))
        .replace("__BINARY__", binary);
      writeFileSync(join(outDir, "sw.js"), source);
    },
  };
}

/** builtFiles lists every file under dir, as URL paths relative to it. */
function builtFiles(dir: string): string[] {
  return readdirSync(dir, { recursive: true })
    .map(String)
    .filter((name) => statSync(join(dir, name)).isFile())
    .map((name) => name.split(sep).join("/"))
    .sort();
}

/**
 * wasmPreviewGzip compresses the WebAssembly binary on the preview server.
 *
 * `vite preview` gzips the script and the stylesheet but hands the binary over
 * raw — 3.3 MB against the ~950 KB a real host sends,
 * in the one asset everything waits on.
 * Without this, preview reads seconds slower than the site it stands in for.
 *
 * Only preview. In dev the binary comes from src/ over a local round trip.
 */
function wasmPreviewGzip(): Plugin {
  let outDir = "";

  return {
    name: "wasm-preview-gzip",

    configResolved(config) {
      outDir = resolve(config.root, config.build.outDir);
    },

    configurePreviewServer(server) {
      server.middlewares.use((request, response, next) => {
        const path = request.url?.split("?")[0] ?? "";
        const accepted = request.headers["accept-encoding"] ?? "";
        if (!path.endsWith(".wasm") || !String(accepted).includes("gzip")) {
          next();
          return;
        }

        // Nothing outside the built directory.
        const file = resolve(join(outDir, decodeURIComponent(path)));
        if (!file.startsWith(outDir) || !existsSync(file)) {
          next();
          return;
        }

        response.setHeader("Content-Type", "application/wasm");
        response.setHeader("Content-Encoding", "gzip");
        response.setHeader("Vary", "Accept-Encoding");
        createReadStream(file).pipe(createGzip()).pipe(response);
      });
    },
  };
}

export default defineConfig({
  // Relative asset URLs: GitHub Pages project sites serve from /<repo>/.
  base: "./",
  // One page, no client-side routing. Vite otherwise answers unmatched paths
  // with index.html where the static host 404s, and an audit run against the
  // dev server reports hundreds of syntax errors in a /robots.txt that doesn't exist.
  appType: "mpa",
  plugins: [goWasmWatch(), wasmLoader(), serviceWorker(), wasmPreviewGzip()],
  build: {
    target: "es2022",
    assetsInlineLimit: 0,
  },
});
