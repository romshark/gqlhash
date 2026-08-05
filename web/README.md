# gqlhash web

A static, CDN-hostable playground for [gqlhash](../): two tabbed editors — the
operations a client sends on the left, the
[gqlhash-proxy](../cmd/gqlhash-proxy/README.md) allowlist directory on the right
— and the verdict the proxy would give. There's no backend: the Go hasher is
compiled to WebAssembly. Nothing leaves the browser. State is kept in
`localStorage`.

## Requirements

- Go (the version in [go.mod](../go.mod)), for the WebAssembly build
- Node 20+ and [pnpm](https://pnpm.io)

## Development

```sh
pnpm install
pnpm dev
```

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
| `pnpm run ci`    | What CI checks: `biome ci .` plus the typecheck        |

Go edits hot-reload too; compile errors go to the terminal running `pnpm dev`.

Measure against `pnpm preview`, never `pnpm dev`. The dev server ships every
module unbundled and unminified — about 7 MB against the build's 1.2 MB — so a
Lighthouse run there scores around 70 where the build scores 96, all of it the
harness.

[Biome](https://biomejs.dev) handles formatting, linting and import sorting. Go
code is unaffected — `gofmt` and `go vet` from the repository root as usual.

## Layout

| Path                       | Contains                                              |
| -------------------------- | ----------------------------------------------------- |
| `wasm/main.go`             | Go entry point; exports the `gqlhash` global to JS     |
| `src/gqlhash.ts`           | Loads the binary and wraps that global in a typed API  |
| `src/editor.ts`            | CodeMirror setup, theme and error markers              |
| `src/graphql-language.ts`  | GraphQL syntax highlighting for CodeMirror             |
| `src/pane.ts`              | One tabbed editor: the files, their tabs and the swap  |
| `src/main.ts`              | Splash screen, controls, the lookup and the verdict    |
| `src/example.ts`           | The files the two panes start with                     |
| `src/storage.ts`           | Reading and writing them in the browser                |
| `src/style.css`            | The palette, the Morpheus theming, the frame           |
| `src/vendor/morpheus/`     | The vendored UI kit, its own README beside it          |
| `public/icons/`            | The SVGs `<neo-icon>` fetches                          |

`src/wasm/gqlhash.wasm` and `src/vendor/wasm_exec.js` are build outputs of
[scripts/build-wasm.sh](scripts/build-wasm.sh) and aren't checked in.
`wasm_exec.js` is copied from the Go toolchain in use. It has to match the
compiler that produced the binary.

The controls mirror the flags of `cmd/gqlhash` (`-hash`, `-format`, `-ignore`)
and apply to both panes at once. The lists of hash functions and formats come
from the Go side at runtime. The page can't drift out of sync with what the
library supports.

## Morpheus

The frame is built from [Morpheus](https://github.com/romshark/morpheus), a web
component UI kit, vendored rather than installed: two files in
[src/vendor/morpheus](src/vendor/morpheus), where
[that README](src/vendor/morpheus/README.md) records the version, the commit and
what to copy to update.

Two things the kit leaves to the user: the palette (`style.css` sets both the
role tokens and the `--neo-*` layer its components read) and the icons
(`<neo-icon>` fetches `<name>.svg` from `--neo-icon-base`; the Lucide SVGs the
page uses are in [public/icons](public/icons)).

## Colors

The palette is Rhodamine — `#E10098`, the one color the
[GraphQL brand guidelines](https://graphql.org/brand/) publish — split across
three tokens in [src/style.css](src/style.css): `--brand` (Rhodamine in both
schemes, wordmark and favicon only), `--accent` (interface accent, lightened in
dark where 4.1:1 isn't enough) and `--accent-text` (body text, 4.5:1 either
way). Every color, including CodeMirror's, is a variable in the same block.

The two grey tokens carry a floor of their own: `--fg-muted` and `--fg-faint`
are set to clear 4.5:1 against every surface they land on, the tightest being
`--bg-raised` in dark and `--bg-input` in light. `--fg-faint` is the smallest
text on the page — the option hints, the footer, the tab markers — and
`--syn-comment` matches it: a comment in the editor is text like any other.

[src/theme.ts](src/theme.ts) resolves "follow the system" itself rather than
leaving it to a media query, which a choice can't overrule. The key and class
names it saves under are repeated by hand in the pre-paint script in
[index.html](index.html); the two have to agree.

## Icons and the manifest

`public/` holds the app icons and a
[web app manifest](public/manifest.webmanifest): `favicon.svg` and
`favicon.ico`, `icon.svg` with its 192/512 PNGs, a maskable pair, and
`apple-touch-icon.png`. The three SVGs are the source of truth. They draw their
glyph as strokes rather than a `#` character, which leaves no font to substitute
when they're rasterized.

Regenerate the raster sizes after editing an SVG. The outputs are committed;
neither CI nor `pnpm build` needs a rasterizer:

```sh
brew install librsvg imagemagick
./scripts/build-icons.sh
```

The page works offline after one visit. [src/sw.js](src/sw.js) is the worker;
`pnpm build` writes it to `dist/sw.js` with the file list of that build baked
in. `pnpm dev` has none. Test offline against `pnpm preview`.

## Deployment

`pnpm build` writes a self-contained `dist/` servable from any static host or
CDN, including a GitHub Pages project site. All asset URLs are relative
(`base: "./"` in [vite.config.ts](vite.config.ts)).
[../.github/workflows/pages.yml](../.github/workflows/pages.yml) publishes it on
every push to `main` touching the site or the hasher; it needs Pages enabled with
"GitHub Actions" as the source.

The WebAssembly binary is ~3.3 MB, or ~950 KB over the wire — make sure the host
serves `.wasm` with `Content-Encoding: gzip` or `br`. GitHub Pages does by default.
