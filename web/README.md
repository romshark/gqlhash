# gqlhash web

A static, CDN-hostable playground for [gqlhash](../): two tabbed editors — the
operations a client sends on the left, the
[gqlhash-proxy](../cmd/gqlhash-proxy/README.md) allowlist directory on the right
— and the verdict the proxy would give. There's no backend: the Go hasher is
compiled to WebAssembly, so nothing leaves the browser. State is kept in
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

Go edits are covered by hot reloading too: the `go-wasm-watch` plugin in
[vite.config.ts](vite.config.ts) watches [wasm/](wasm/) and the parent-module
packages it imports, rebuilds, and reloads the page. Compile errors go to the
terminal running `pnpm dev`.

[Biome](https://biomejs.dev) handles formatting, linting and import sorting;
[biome.json](biome.json) excludes the vendored files, none of which are ours to
reformat. Go code is unaffected — `gofmt` and `go vet` from the repository root
as usual.

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
`wasm_exec.js` is copied from the Go toolchain in use, because it has to match
the compiler that produced the binary.

The controls mirror the flags of `cmd/gqlhash` (`-hash`, `-format`, `-ignore`)
and apply to both panes at once. The lists of hash functions and formats come
from the Go side at runtime, so the page can't drift out of sync with what the
library supports.

## Morpheus

The frame is built from [Morpheus](https://github.com/romshark/morpheus), a web
component UI kit, vendored rather than installed: two files in
[src/vendor/morpheus](src/vendor/morpheus), where
[that README](src/vendor/morpheus/README.md) records the version, the commit and
what to copy to update. `bundle.js` is imported for its side effect from
`main.ts`; `morpheus.css` is `@import`ed at the top of
[src/style.css](src/style.css), so this page's rules can override it.

Two things the kit leaves to the user: the palette (`style.css` sets both the
role tokens and the `--neo-*` layer its components read) and the icons
(`<neo-icon>` fetches `<name>.svg` from `--neo-icon-base`, so the Lucide SVGs
the page uses are in [public/icons](public/icons)).

## Colors

The palette is Rhodamine — `#E10098`, the one color the
[GraphQL brand guidelines](https://graphql.org/brand/) publish — split across
three tokens in [src/style.css](src/style.css): `--brand` (Rhodamine in both
schemes, wordmark and favicon only), `--accent` (interface accent, lightened in
dark where 4.1:1 isn't enough) and `--accent-text` (body text, 4.5:1 either
way). Every color, including CodeMirror's, is a variable in the same block.

[src/theme.ts](src/theme.ts) puts `dark` or `light` on `<html>` — resolving
"follow the system" itself, since a media query can't be overruled by a choice —
and saves the choice under `gqlhash-theme`. An inline script in
[index.html](index.html) reads that key before first paint to avoid a white
flash; it and `theme.ts` agree on the key and class names by hand.

## Icons and the manifest

`public/` holds the app icons and a
[web app manifest](public/manifest.webmanifest): `favicon.svg` and
`favicon.ico`, `icon.svg` with its 192/512 PNGs, a maskable pair, and
`apple-touch-icon.png`. The three SVGs are the source of truth and draw their
glyph as strokes, not a `#` character, so there's no font to substitute when
they're rasterized.

Regenerate the raster sizes after editing an SVG; the outputs are committed, so
neither CI nor `pnpm build` needs a rasterizer:

```sh
brew install librsvg imagemagick
./scripts/build-icons.sh
```

This is installable metadata only — there's no service worker, so the page
doesn't work offline.

## Deployment

`pnpm build` writes a self-contained `dist/` servable from any static host or
CDN, including a GitHub Pages project site, since all asset URLs are relative
(`base: "./"` in [vite.config.ts](vite.config.ts)).
[../.github/workflows/pages.yml](../.github/workflows/pages.yml) publishes it on
every push to `main` touching the site or the hasher; it needs Pages enabled with
"GitHub Actions" as the source.

The WebAssembly binary is ~3.3 MB, or ~950 KB over the wire — make sure the host
serves `.wasm` with `Content-Encoding: gzip` or `br`. GitHub Pages does by
default.
