# Migrating from v1 to v2

v2 changes the hash of some documents, refuses some documents v1 accepted, and
renames most of the Go API. Read [Hashes](#hashes) before upgrading anything
that stored one.

The import path carries the major version, so nothing moves until you change it:

```go
import "github.com/romshark/gqlhash/v2"
```

```sh
go install github.com/romshark/gqlhash/v2/cmd/gqlhash@latest
go install github.com/romshark/gqlhash/v2/cmd/gqlhash-proxy@latest
```

## Hashes

**A document whose string values hold an escape sequence hashes differently
under v2. Everything else hashes the same.**

v1 hashed a string as it was spelled; v2 hashes the value it stands for, so
`"\u0041"` and `"A"` now agree with each other and neither agrees with v1.

```graphql
{ user(id: 1) { name } }        # same hash under v1 and v2
{ f(a: "plain text") }          # same hash under v1 and v2
{ f(path: "C:\\dir") }          # different hash under v2
{ f(a: "line\nbreak") }         # different hash under v2
{ f(a: """a\"""b""") }          # different hash under v2
```

What that means where a hash was kept:

- **An allowlist directory needs nothing.** `gqlhash-proxy` hashes the `.graphql`
  files itself at startup, so the entries are rebuilt from the documents.
- **A stored hash needs rebuilding**: a persisted query registry, a cache key
  written to a shared cache, a hash committed to a repository for a CI check.
  Rehash the documents with v2 before serving with it, or the entries stop
  matching what clients send.

Nothing warns about this. A hash that no longer matches reads as a document
that isn't allowed.

## Documents v1 accepted and v2 refuses

v2 checks lexical rules v1 didn't. Three cases turn a document that used to
hash into one that's refused:

| document | why v2 refuses it |
| --- | --- |
| `{ f(a: "\ud800") }` | a lone surrogate is no Unicode scalar value, `ErrInvalidEscape` |
| `query Q($x: Int = $y) { f }` | a variable where the grammar asks for a constant, `ErrUnexpectedVariable` |
| nesting past 128 levels | `ErrTooDeep`, see `-depth-limit` |

The depth limit is new and on by default. It's past what a document written for
an API reaches, but a generated or deeply nested one may need `-depth-limit`
raised.

In the proxy this shows up as a **skipped allowlist entry**, logged at startup
and on reload, not as a failure to start. Read the reload output — `POST
/reload` answers with `skipped.errors` naming every file left out.

## The gqlhash command

Everything v1 took still works and means the same thing. What's new:

| flag | |
| --- | --- |
| `-ignore` | leave input values or variables out of the hash, see the README |
| `-depth-limit` | how deeply a document may nest, 128 by default |
| `-format base64url` | URL-safe base64, alongside `hex`, `base32` and `base64` |
| `-hash blake3`, `fnv1a`, `xxh64` | three more functions |

A syntax error now names where it stopped:

```
v1: syntax error: unexpected EOF
v2: <stdin>:1:8: syntax error: unexpected EOF
```

## The Go API

Every entry point takes `Options` and a `string` or `[]byte`, and reports
failure as a [`Result`](https://pkg.go.dev/github.com/romshark/gqlhash/v2#Result)
value rather than an `error`.

| v1 | v2 |
| --- | --- |
| `AppendQueryHash(buf, h, doc)` | `AppendHash(buf, h, options, doc)` |
| `Compare(h, a, b) error` | `Compare(h, options, a, b) (bool, Result)` |
| `CompareWithBuffer(buf, h, a, b) error` | `CompareWithBuffer(buf, h, options, a, b) (bool, Result)` |
| `ErrQueriesDiffer` | gone: the first return says whether they match |
| — | `NewHasher` / `Hasher`, which allocates nothing per call |

`Compare` no longer reports "these differ" as an error:

```go
// v1
if err := gqlhash.Compare(sha1.New(), a, b); err != nil {
    if errors.Is(err, gqlhash.ErrQueriesDiffer) { /* differ */ }
    /* or a syntax error */
}

// v2
equal, err := gqlhash.Compare(sha1.New(), gqlhash.Options{}, a, b)
if err.IsErr() {
    /* a syntax error, err.Err is the sentinel and err.ErrOffset says where */
}
if !equal { /* differ */ }
```

`Result` is not an `error`. Its zero value means nothing failed, so check
`Err` or `IsErr`, and pass `Result.Err` to `errors.Is`:

```go
if errors.Is(err.Err, gqlhash.ErrTooDeep) { /* ... */ }
```

[`Position`](https://pkg.go.dev/github.com/romshark/gqlhash/v2#Position) turns
`Result.ErrOffset` into a line and a column where a message needs one — v1
carried them in the error, v2 computes them only where they're printed.

There are five more sentinels to match on: `ErrUnexpectedVariable`,
`ErrInvalidEscape`, `ErrMalformedNumber`, `ErrMalformedUTF8` and
`ErrUnescapedControlChar`. Each wraps `ErrUnexpectedToken`, so code matching
only that one keeps working.

### The parser package

`parser` no longer exposes the reader functions v1 had — `ReadDocument`,
`ReadDefinition`, `ReadSelectionSet`, the `Is*` predicates and the rest. v2
reads a document with one call:

```go
err := parser.Parse(w, parser.Options{}, document) // w is an io.Writer
```

Use [`parser.Parser`](https://pkg.go.dev/github.com/romshark/gqlhash/v2/parser#Parser)
where this runs per request; it reuses its buffers and allocates nothing.

The `HPref*` prefixes are still there and are now `byte` constants rather than
`[]byte` variables.
