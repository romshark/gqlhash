import { spawn } from "node:child_process";
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

export default defineConfig({
  // Relative asset URLs so the build can be served from any path, which is what
  // GitHub Pages project sites (/<repo>/) need.
  base: "./",
  plugins: [goWasmWatch()],
  build: {
    target: "es2022",
    assetsInlineLimit: 0,
  },
});
