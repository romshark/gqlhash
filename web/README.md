# gqlhash web

A static, CDN-hostable page that runs [gqlhash](../) in the browser: type a
GraphQL document into the editor and its hash is recomputed 200 ms after you
stop typing, and immediately whenever an option changes.

There's no backend. The Go hasher is compiled to WebAssembly and everything —
parsing, hashing, encoding — happens on the client, so no document ever leaves
the browser.

## Requirements

- Go (the version in [go.mod](../go.mod)), for the WebAssembly build
- Node 20+ and [pnpm](https://pnpm.io)

## Development

```sh
pnpm install
pnpm dev
```

`pnpm dev` compiles the WebAssembly binary and then starts Vite with live
reloading. TypeScript and CSS edits are swapped in without a page reload — the
stylesheet is linked from [index.html](index.html) rather than imported from
`main.ts`, which keeps it render-blocking so the splash screen never paints
unstyled, and Vite still hot-updates the link. Go edits are covered too: the
`go-wasm-watch` plugin in
[vite.config.ts](vite.config.ts) watches [wasm/](wasm/) and the packages of the
parent module it imports, rebuilds the binary, and the page reloads once the
build finishes. Compile errors are printed to the terminal running `pnpm dev`.

| Command          | Does                                                   |
| ---------------- | ------------------------------------------------------ |
| `pnpm dev`       | Build the WASM binary, then serve with hot reloading    |
| `pnpm wasm`      | Rebuild only `src/wasm/gqlhash.wasm`                   |
| `pnpm build`     | Build the WASM binary, typecheck and bundle to `dist/` |
| `pnpm preview`   | Serve `dist/` as it will be served in production       |
| `pnpm typecheck` | Run `tsc --noEmit`                                     |
| `pnpm lint`      | Report formatting, lint and import-order problems       |
| `pnpm lint:fix`  | Fix everything above that's safely fixable             |
| `pnpm format`    | Format only, no lint rules                             |
| `pnpm ci`        | What CI runs: `biome ci .` plus the typecheck          |

## Linting

[Biome](https://biomejs.dev) handles formatting, lint rules and import sorting
for the TypeScript, CSS and JSON in here; [biome.json](biome.json) is the
config, on the recommended rule set. It reads `.gitignore`, so the generated
`src/wasm/` and `dist/` are skipped, and `src/vendor/wasm_exec.js` is excluded
explicitly because it ships with the Go toolchain and isn't ours to reformat.

CI runs `biome ci .`, which never writes; use `pnpm lint:fix` locally. Go code
is unaffected — use `gofmt` and `go vet` from the repository root as usual.

## Deployment

`pnpm build` writes a fully self-contained `dist/` that can be served from any
static host or CDN — including a GitHub Pages project site, since all asset URLs
are relative (`base: "./"` in [vite.config.ts](vite.config.ts)).

[../.github/workflows/pages.yml](../.github/workflows/pages.yml) builds and
publishes it on every push to `main` that touches the site or the hasher. It
needs Pages enabled for the repository with "GitHub Actions" as the source
(Settings → Pages → Build and deployment).

The WebAssembly binary is ~3.3 MB, or ~950 KB over the wire once the host
compresses it. Make sure the host serves `.wasm` with `Content-Encoding: gzip`
or `br`; GitHub Pages does this by default. A splash screen reports the download
progress while it loads.

## Icons and the manifest

`public/` holds the icons and a
[web app manifest](public/manifest.webmanifest), so the page can be installed
and pinned with a real icon rather than a default one:

| File                                     | Used for                                        |
| ---------------------------------------- | ----------------------------------------------- |
| `favicon.svg`                            | Tab icon; follows light/dark on its own          |
| `favicon.ico` (16/32/48)                 | Fallback for browsers without SVG icon support   |
| `icon.svg`, `icon-192/512.png`            | Manifest icons for unmasked contexts            |
| `icon-maskable.svg`, `-512.png`           | Manifest icons for platforms that apply a mask  |
| `apple-touch-icon.png` (180)             | iOS home screen                                 |

The three SVGs are the source of truth and their glyph is drawn as strokes, not
as a `#` character, so there's no font to substitute when they're rasterized.
The favicon carries its own `prefers-color-scheme` rule — brand magenta on a
light tab strip, the lighter tint on a dark one — and `<meta name="theme-color">`
is declared twice with `media` so the browser UI is tinted per scheme too.

Regenerate the raster sizes with [scripts/build-icons.sh](scripts/build-icons.sh)
after editing an SVG:

```sh
brew install librsvg imagemagick
./scripts/build-icons.sh
```

The outputs are committed, so neither CI nor `pnpm build` needs a rasterizer.

Note that this is installable metadata only — there's no service worker, so the
page doesn't work offline and browsers that require one won't offer to install
it. Adding [vite-plugin-pwa](https://vite-pwa-org.netlify.app/) would cover
that; precaching the ~950 KB WebAssembly binary is the main thing it would buy.

## Colors

The palette is the GraphQL brand magenta (`#e10098`) on a mostly white base,
with a dark counterpart that swaps in a lighter tint of the same hue. Which one
you get follows the operating system through `prefers-color-scheme`; there's no
toggle. Every color is a CSS variable declared in
[src/style.css](src/style.css), including the ones CodeMirror uses, so the
editor stays in step — the accent is deliberately split into `--accent` (the
brand color as published, for the wordmark, caret and focus rings) and
`--accent-text` (darkened enough to read as body text on white).

## Layout

| Path                       | Contains                                              |
| -------------------------- | ----------------------------------------------------- |
| `wasm/main.go`             | Go entry point; exports the `gqlhash` global to JS     |
| `src/gqlhash.ts`           | Loads the binary and wraps that global in a typed API  |
| `src/editor.ts`            | CodeMirror setup, theme and error markers              |
| `src/graphql-language.ts`  | GraphQL syntax highlighting for CodeMirror             |
| `src/main.ts`              | Splash screen, controls and the debounced rehash       |

`src/wasm/gqlhash.wasm` and `src/vendor/wasm_exec.js` are build outputs of
[scripts/build-wasm.sh](scripts/build-wasm.sh) and aren't checked in.
`wasm_exec.js` is copied from the Go toolchain in use, because it has to match
the compiler that produced the binary.

## The options

The controls mirror the flags of `cmd/gqlhash`:

- **Hash function** — `-hash`, all twelve the CLI supports.
- **Output format** — `-format`: hex, base32, base64 or base64url.
- **`-ignore-inputs`** — hash argument and default values as if they weren't
  there, keeping only the variable signature.
- **`-ignore-variables`** — ignore variable definitions and usages entirely.
  This implies `-ignore-inputs`, so that box is checked and disabled while it's
  on, and your own setting comes back when you switch it off.

The list of hash functions and formats comes from the Go side at runtime, so the
page can't drift out of sync with what the library actually supports.

## Syntax errors

A document that doesn't parse shows the parser's message next to the hash, in
place of a hash.

Whether the editor also points at the offending spot depends on what the parser
reports. If the error carries an offset, line and column, the gutter gets an
error marker and the token is underlined; the version this builds against
returns the message alone, so no marker is shown. That's deliberate — marking an
arbitrary line would claim the problem is somewhere it isn't, which is worse
than marking nothing. The page needs no change to take advantage of positions
once the parser reports them: `offset`, `line` and `column` are already optional
fields on the result.
