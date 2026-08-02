# gqlhash-proxy

Part of [gqlhash](../../README.md), which the `gqlhash` CLI and the hashing rules belong to. This file is the proxy: what it serves, every flag it takes and what a deployment has to know. [`gqlhash-proxy-fhttp`](../gqlhash-proxy-fhttp/README.md) is the same proxy served with fasthttp and takes everything here as written — it is **experimental and not for production use**.

`gqlhash-proxy` serves an allowlist of documents in front of a GraphQL API. A request whose document is on the list is forwarded, every other request is rejected with `403 Forbidden` and never reaches the API.

```sh
gqlhash-proxy \
  -server.listen :8080 \
  -upstream.url http://api:4000/graphql \
  -allowlist ./queries \
  -control.listen 127.0.0.1:9090
```

`-server.listen` serves the proxy on every path: the request path is never routed on, and `-upstream.url` is the endpoint the forwarded request reaches. The document is read where [GraphQL over HTTP](https://graphql.github.io/graphql-over-http/draft/) puts it: the `query` parameter of a `GET`, or the `query` member of an `application/json` body. That specification is a working draft and defines neither an `application/graphql` body nor batching; the proxy reads both anyway, the first as the document itself, the second under `-allow-batch`.

`-allowlist` is a directory of `.graphql` and `.gql` files holding the allowed documents. The proxy hashes them itself, so the documents are the source of truth. Formatting and comments may differ between a file and what a client sends. The set of definitions may not: one file is one entry.

A `.graphqls` file in the same directory is read as the schema, and every document is then checked against it: one asking for a field the schema doesn't have is skipped like one that doesn't parse. Without such a file nothing is checked against a schema. Several `.graphqls` files are read as one schema, and a schema that doesn't parse is reported and leaves the documents unchecked rather than unserved.

The proxy rejects ambiguous requests with `400 Bad Request`. Both keys in `{"query":"<allowed>","quer\u0079":"<anything>"}` unescape to `query`. The proxy can't tell which document would reach the API, so it hashes neither and refuses. Same for a GET naming `query` twice, percent-encoded or not. A `GET` carrying a body is the same case: its document is the query parameter, and a body is a second place one could be.

Two files whose documents hash alike are both skipped: which one a request meant is unknowable, and allowing the wrong one is worse than allowing neither.

A document that doesn't parse is skipped with an error log, at startup and on reload alike, so one broken file doesn't keep the rest from being served. A directory with no usable document serves an empty allowlist, rejects everything.

`-control.listen 127.0.0.1:9090` serves the control server on that address, which is separate from the port that serves traffic. It provides [Prometheus](https://prometheus.io/) metrics on `/metrics` and rereads the allowlist on `POST /reload`.

`/status` answers what the proxy has decided so far: the size of the allowlist, when it was loaded, and the counters for every decision and for upstream failures. Each refusal is counted apart: `rejected` for a document that isn't on the list, `malformed` for a request that carries none, `too_large` past `-server.max-body`, `ambiguous` for one naming its document twice, `too_deep` past the depth limit. Like the metrics it needs no token, and like them it isn't served on the traffic port — it's operational state, not something a client of the API should see.

`GQLHASH_PROXY_CONTROL_TOKEN` requires `Authorization: Bearer <token>` on `/reload`, compared in constant time. Metrics are served without it. There is no flag for the token: a process argument is readable by anyone on the host through `ps` or `/proc/<pid>/cmdline`, the environment of a process isn't.

## Reloading Allowlist

```sh
# Control server host
curl -fsS -X POST localhost:9090/reload
```

Returns:

```json
{
  "documents": {
    "total": 2,
    "files": ["queries/list-users.graphql", "queries/user-with-email.graphql"]
  },
  "skipped": {
    "total": 1,
    "errors": ["queries/get-user.graphql:2:9: unexpected token: malformed number"]
  }
}
```

Every flag of the proxy, with its default:

| Flag | Default | |
| --- | --- | --- |
| `-upstream.url` | required | the GraphQL API a request is forwarded to |
| `-allowlist` | required | the directory the documents are read from |
| `-server.listen` | `:8080` | where the traffic is served |
| `-control.listen` | `127.0.0.1:9090` | where `/metrics`, `/status` and `/reload` are served |
| `-hash` | `sha2` | `sha2`, `sha3`, `blake2b`, `blake2s` or `blake3` |
| `-ignore` | `nothing` | what to leave out of the hash, see [Ignoring Input Values](../../README.md#ignoring-input-values) |
| `-server.max-body` | 1 MiB | largest request body accepted |
| `-depth-limit` | 128 | how deeply a document may nest before it's refused, counted as `too_deep` |
| `-allow-batch` | off | accept batches, where every document has to be allowed |
| `-opaque-errors` | off | answer every rejection with 403 and no detail |
| `-trust-forwarded` | off | keep the `X-Forwarded-*` headers a request arrives with |
| `-log.level` | `info` | `debug`, `info`, `warn` or `error` |
| `-log.json` | on | JSON instead of readable text |
| `-log.requests` | off | log every forwarded request at debug level |
| `-upstream.timeout` | 30s | how long an upstream request may take |
| `-upstream.max-idle-conns-per-host` | 64 | connections kept open to the upstream |
| `-upstream.max-idle-conns` | 256 | ceiling over the per-host pool, 0 for none |
| `-upstream.max-conn-lifetime` | off | retire an upstream connection once it's this old |
| `-upstream.http2` | on | allow HTTP/2 to an `https` upstream |
| `-server.read-header-timeout` | 10s | how long a client may take to send the headers |
| `-server.read-timeout` | 30s | how long a client may take to send the request |
| `-server.write-timeout` | `-upstream.timeout` + 10s | how long answering may take |
| `-server.idle-timeout` | 2m | how long an idle keep-alive connection is held |
| `-server.shutdown-timeout` | 10s | how long the requests in flight are waited for |

`0` leaves any of the timeouts off. Four of them carry a constraint worth knowing:

- `-server.write-timeout` must stay above `-upstream.timeout`, or the proxy cuts off a response the upstream is still allowed to be sending. It follows that flag unless you set it, and a value at or below it is rejected at startup.
- `-server.read-timeout` has to fit `-server.max-body` arriving over the slowest link you serve, and must not be below `-server.read-header-timeout`, which would leave that one without effect.
- `-server.idle-timeout` belongs above the idle timeout of any load balancer in front, or the balancer reuses a connection the proxy is closing.
- `-upstream.max-idle-conns-per-host` is what caps connection reuse: there is one upstream, so every forwarded request draws from that one pool. It belongs at or above the requests you serve at once, or the surplus connections are dialed again per request, see [tuning](../../TUNING_GQLHASH_PROXY.md).
- `-upstream.max-conn-lifetime` belongs on wherever the upstream is several backends behind one name. They're balanced per connection, so a pool that never turns over never reaches one added after it filled, and a large `-upstream.max-idle-conns-per-host` makes that worse rather than better.
- [`gqlhash-proxy-fhttp`](../gqlhash-proxy-fhttp/README.md) takes the same flags and serves them with fasthttp, forwarding around 59% faster on a third of the memory. It is **experimental and not for production use**: it trades cancellation on client disconnect, HTTP/2, a well-worn HTTP parser, and it hands a client's request trailer to your API as an ordinary header. [GQLHASH_PROXY_FHTTP.md](../../GQLHASH_PROXY_FHTTP.md) has the whole trade, measured.

`-hash` takes only the collision-resistant functions, unlike `gqlhash`. Rationale: an allowlist's security property is collision resistance, and `crc32`, `crc64`, `fnv`, `fnv1a` and `xxh64` are collidable by construction while `md5` and `sha1` are broken.

`-trust-forwarded` appends the peer to the `X-Forwarded-*` headers instead of replacing them, which a proxy behind a load balancer needs so the API still sees the original client. Set it only there: a client that connects directly can otherwise claim any address.

Exposed on `/metrics` are request counters by decision, upstream errors, the allowlist size and load time, a request duration histogram, and the Go runtime collectors. Rejections are counted, not logged: a flood of them would otherwise write one line each, so those events sit at debug level.

Every flag can be given through the environment instead, as `GQLHASH_PROXY_` followed by its name with the dashes and dots as underscores: `GQLHASH_PROXY_SERVER_MAX_BODY=4096`, `GQLHASH_PROXY_UPSTREAM_URL=http://api:4000/graphql`. A flag given on the command line wins. `gqlhash` reads no environment, so a variable can't quietly change the hashes a pipeline produces.

[TUNING_GQLHASH_PROXY.md](../../TUNING_GQLHASH_PROXY.md) has the throughput and latency of both paths, what moves the forwarded one, and how to measure it without measuring the load generator instead. `go run ./internal/cmd/loadtest` runs it.

[playground](../../playground) runs the proxy in front of a sample GraphQL API with `docker compose up --build`, with a few allowed documents and the schema they're checked against.

## Request shapes with no reading here

Two shapes a GraphQL client may well produce carry no document this can hash, so both are `400` and neither reaches the API:

- **`multipart/form-data`**, the [GraphQL multipart request spec](https://github.com/jaydenseric/graphql-multipart-request-spec) used for file uploads. The document is a field inside the multipart body rather than the body itself.
- **An APQ request carrying only `extensions.persistedQuery`**, with no `query` member. Automatic persisted queries are the client asking the *API* to look a hash up; this proxy hashes documents itself, and there's nothing to hash in such a request.

Both are correct by construction — there is no document — but neither is a shape you can serve through this proxy. `TestShapesRealClientsSendThatAreRefused` pins them.

## No protocol upgrade passes through

`Upgrade` and the `Connection` token naming it are dropped on the way upstream, and a `101` from the API is a `502` rather than a relayed switch. A tunnel is a channel the proxy can hash once and never again, which is the one thing an allowlist in front of an API can't allow. `Upgrade: h2c`, which `curl --http2` offers in cleartext, is dropped the same way and the request is served over HTTP/1.1.

Subscriptions over **WebSocket** therefore can't reach an API through this proxy. Subscriptions over **SSE** can — see [Subscriptions](#subscriptions).

## Subscriptions

Use [GraphQL over SSE](https://github.com/enisdenjo/graphql-sse) in **distinct connections mode**, which is what [GraphQL Yoga](https://the-guild.dev/graphql/yoga-server/docs/features/subscriptions) defaults to. Every operation is an ordinary `POST` or `GET` carrying an ordinary document, so a subscription is hashed and checked against the allowlist exactly like a query — put the subscription document in the allowlist directory and it is served.

The answer is relayed event by event for as long as the API sends events. `-upstream.timeout` and `-server.write-timeout` bound an exchange, and a stream is not one, so neither cuts a subscription short; the timeout still bounds the wait for the answer's headers.

```sh
curl -N -H 'Accept: text/event-stream' -H 'Content-Type: application/json' \
  -d '{"query":"subscription { ticks }"}' http://localhost:8080/graphql
```

**Single connection mode is not supported.** Its `PUT` reservation, its stream-opening `GET` and its `DELETE` carry no GraphQL document, and forwarding a request the allowlist never saw is the one thing this proxy is for. All three are answered `400`.

**WebSocket** (`graphql-ws`) can't work here at all: after the upgrade the frames are opaque, so one allowlisted handshake would buy an unhashed channel. Route it around the proxy at your ingress if you need it.
