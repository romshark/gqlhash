import { spawn } from "node:child_process";
import { createReadStream, existsSync } from "node:fs";
import { join, resolve } from "node:path";
import { createGzip } from "node:zlib";
import { defineConfig, type Plugin } from "vite";

/**
 * goWasmWatch rebuilds src/wasm/gqlhash.wasm whenever a Go source it's built
 * from changes. Vite already watches the resulting binary, so the page reloads
 * on its own once the build finishes.
 */
function goWasmWatch(): Plugin {
  // Everything the wasm binary is compiled from: its own entry point plus the
  // packages of the parent module it imports.
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
            // Report the compile error instead of leaving a stale binary in
            // place with no explanation.
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
 * wasmPreload puts a preload hint for the WebAssembly binary in the head.
 *
 * Without it the binary is at the end of a chain: the browser has to fetch the
 * document, then the entry chunk, then parse and run it before the fetch in
 * gqlhash.ts is even issued — and the binary is by far the largest thing the
 * page loads, so nothing real can paint until it lands. The hint starts it
 * alongside the script instead of after it.
 *
 * The name can't be written into index.html by hand: the build content-hashes
 * it. The link is emitted here instead, with whichever URL the build produced.
 */
function wasmPreload(): Plugin {
  // The URL of the binary as it will be served. Only known once the bundle is
  // written, which is after the HTML is transformed, so it's collected from
  // the module graph on the way past.
  let href = "";

  return {
    name: "wasm-preload",

    // In dev nothing is hashed and the file is served from its source path.
    configureServer() {
      href = "/src/wasm/gqlhash.wasm";
    },

    generateBundle(_options, bundle) {
      for (const [name, output] of Object.entries(bundle)) {
        if (output.type === "asset" && name.endsWith(".wasm")) {
          href = `./${name}`;
        }
      }
    },

    transformIndexHtml: {
      // After the bundle is generated, so the hashed name is known.
      order: "post",
      handler() {
        if (!href) {
          return [];
        }
        return [
          {
            tag: "link",
            injectTo: "head",
            attrs: {
              rel: "preload",
              href,
              as: "fetch",
              type: "application/wasm",
              // Matches the mode of the plain fetch() in gqlhash.ts,
              // so the response is taken from the preload cache rather than
              // fetched a second time.
              crossorigin: "anonymous",
            },
          },
        ];
      },
    },
  };
}

/**
 * wasmPreviewGzip compresses the WebAssembly binary on the preview server.
 *
 * `vite preview` gzips the script and the stylesheet but hands the binary over raw,
 * and the binary is 3.3 MB against the ~950 KB a real host sends — three
 * quarters of the page's weight, in the one asset everything waits on.
 * Which makes `pnpm preview` a poor stand-in for production precisely where it's
 * being relied on as one: a Lighthouse run against it reads several seconds
 * slower than the deployed site, for a reason that isn't in the deployed site.
 *
 * Only preview. The dev server serves the binary out of src/, where it's the
 * build's own output and the round trip is local anyway.
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

        // Nothing outside the built directory: the name is decoded,
        // then resolved, and has to still be under it.
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
  // Relative asset URLs so the build can be served from any path,
  // which is what GitHub Pages project sites (/<repo>/) need.
  base: "./",
  // One page, no client-side routing. Vite's default is to answer any
  // unmatched path with index.html, which the static host this is deployed to
  // does not: it 404s. The difference is not cosmetic — an audit run against
  // the dev or preview server fetches /robots.txt, is handed the page instead
  // of a 404, and reports several hundred syntax errors in a file that doesn't exist.
  // Serving misses as misses is what makes `pnpm preview` the promise it makes,
  // which is dist/ as production will serve it.
  appType: "mpa",
  plugins: [goWasmWatch(), wasmPreload(), wasmPreviewGzip()],
  build: {
    target: "es2022",
    assetsInlineLimit: 0,
  },
});
