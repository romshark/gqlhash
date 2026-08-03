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

`-server.listen` serves the proxy on every path: the request path is never routed on, and `-upstream.url` is the endpoint the forwarded request reaches. The document is read where [GraphQL over HTTP](https://graphql.github.io/graphql-over-http/draft/) puts it: the `query` parameter of a `GET`, or the `query` member of an `application/json` body. That specification is a working draft and defines neither an `application/graphql` body nor batching; the proxy reads both anyway, the first as the document itself, the second under `-max-batch`.

`-upstream.url` is that endpoint whole, its query included: an API reached as `https://api.example/graphql?env=staging` gets `env=staging` on every forwarded request, ahead of whatever the client sent. Only the path and the query of the incoming request are the proxy's to decide — the path is replaced by the endpoint's, the query is joined onto the endpoint's — so nothing else of the URL is dropped on the way. Two shapes are refused at startup rather than taken and ignored:

- **A `query` parameter.** It would reach the API beside the document the client sent, and which of the two the API reads is the API's business, where the allowlist saw only one of them.
- **Credentials**, as in `https://user:pass@api.example/graphql`. Nothing forwards them: a reverse proxy builds its request through a transport, and the userinfo of a URL becomes an `Authorization` header only in `http.Client`. Taken, they would reach the log and nothing else. They're also refused for the reason `GQLHASH_PROXY_CONTROL_TOKEN` has no flag: a process argument is readable by anyone on the host. An API that wants credentials takes them from a header the client sends.

`-max-batch` is how many documents one request may carry as a JSON array, every one of which has to be allowed. It is `0` by default, which refuses an array outright, and that default is deliberate: a batch turns one request into as many operations as it holds, and the only other bound available is `-server.max-body` in **bytes**. At the default 1 MiB, the smallest allowed element — `{"query":"{foo bar}"}`, 22 bytes with its separator — fits **47,662** of them, so one request that passes the allowlist can ask the API for 47,662 executions while `/status` reports `"allowed":1` and any rate limit in front of the proxy sees a single request. Every document is on the allowlist and none of them is a document nobody registered; what a count bounds is how many of them run per request. Set it to what your clients actually batch. Past it a request is `413` with `BATCH_TOO_LARGE`, counted as `batch_too_large`, and the scan stops at the document that breaks the cap, so a megabyte of them costs the cap and not the megabyte.

`-allowlist` is a directory of `.graphql` and `.gql` files holding the allowed documents. The proxy hashes them itself, so the documents are the source of truth. Formatting and comments may differ between a file and what a client sends. The set of definitions may not: one file is one entry.

A `.graphqls` file in the same directory is read as the schema, and every document is then checked against it: one asking for a field the schema doesn't have is skipped like one that doesn't parse. Without such a file nothing is checked against a schema. Several `.graphqls` files are read as one schema, and a schema that doesn't parse is reported and leaves the documents unchecked rather than unserved.

The proxy rejects ambiguous requests with `400 Bad Request`. Both keys in `{"query":"<allowed>","quer\u0079":"<anything>"}` unescape to `query`. The proxy can't tell which document would reach the API, so it hashes neither and refuses. Same for a GET naming `query` twice, percent-encoded or not. A `GET` carrying a body is the same case: its document is the query parameter, and a body is a second place one could be.

Two files whose documents hash alike are both skipped: which one a request meant is unknowable, and allowing the wrong one is worse than allowing neither.

A document that doesn't parse is skipped with an error log, at startup and on reload alike, so one broken file doesn't keep the rest from being served. A directory with no usable document serves an empty allowlist, rejects everything.

`-server.tls.cert` and `-server.tls.key` serve the traffic port over HTTPS: a PEM certificate, the leaf first and any intermediates after it, and its key. Both or neither — one alone is a start failure, as is a pair that can't be loaded, so a proxy never binds a port it can't serve. Without them the traffic port is plain HTTP, which is what belongs behind a load balancer terminating TLS itself.

```sh
gqlhash-proxy -upstream.url http://api:4000/graphql -allowlist ./queries \
  -server.tls.cert /etc/ssl/proxy.pem -server.tls.key /etc/ssl/proxy.key
```

Served this way `gqlhash-proxy` offers HTTP/2 over ALPN and falls back to HTTP/1.1; [`gqlhash-proxy-fhttp`](../gqlhash-proxy-fhttp/README.md) is HTTP/1.1 only. The control server on `-control.listen` is plain HTTP either way — it belongs on an address a client of the API can't reach.

Most deployments put the proxy beside the API — same host, same pod, or a segment no client of the API reaches — and `http` is what that hop wants: TLS costs a handshake per connection to protect a wire nobody else is on. Reach for `https` where the upstream sits across a boundary you don't control: another cluster, another network, or a hosted GraphQL service.

An `https` upstream has its certificate verified against the host's trust store. `-upstream.tls.ca` names a PEM file to verify it against instead, which is what an upstream behind a private CA needs. It replaces that trust store rather than adding to it, so only the CA you name can vouch for the API; a deployment that needs more than one — a private CA in production and a public one in staging — puts them in the same file. The file is read at startup, so one that's missing or holds no certificate stops the proxy there rather than surfacing later as an upstream that can't be reached.

Naming a CA doesn't relax anything else: the certificate still has to carry the host `-upstream.url` names. There is no flag to skip the check either — a proxy that skipped it is one any machine on the path can stand in for, and every allowed document would go to whoever answered.

```sh
gqlhash-proxy -upstream.url https://api.internal/graphql -allowlist ./queries \
  -upstream.tls.ca /etc/ssl/private-ca.pem
```

`-control.listen 127.0.0.1:9090` serves the control server on that address, which is separate from the port that serves traffic. It provides [Prometheus](https://prometheus.io/) metrics on `/metrics` and rereads the allowlist on `POST /reload`.

`/status` answers what the proxy has decided so far: the size of the allowlist, when it was loaded, and the counters for every decision and for upstream failures. Each refusal is counted apart: `rejected` for a document that isn't on the list, `malformed` for a request that carries none, `too_large` past `-server.max-body`, `ambiguous` for one naming its document twice, `too_deep` past the depth limit, `batch_too_large` past `-max-batch`. Like the metrics it needs no token, and like them it isn't served on the traffic port — it's operational state, not something a client of the API should see.

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
| `-upstream.url` | required | the GraphQL API a request is forwarded to, its query included |
| `-allowlist` | required | the directory the documents are read from |
| `-server.listen` | `:8080` | where the traffic is served |
| `-server.tls.cert` | off | PEM certificate to serve the traffic port over HTTPS with, needs `-server.tls.key` |
| `-server.tls.key` | off | PEM private key for `-server.tls.cert` |
| `-control.listen` | `127.0.0.1:9090` | where `/metrics`, `/status` and `/reload` are served |
| `-hash` | `sha2` | `sha2`, `sha3`, `blake2b`, `blake2s` or `blake3` |
| `-ignore` | `nothing` | what to leave out of the hash, see [Ignoring Input Values](../../README.md#ignoring-input-values) |
| `-server.max-body` | 1 MiB | largest request body accepted |
| `-depth-limit` | 128 | how deeply a document may nest before it's refused, counted as `too_deep` |
| `-max-batch` | 0 (off) | documents a batched request may carry, every one of which has to be allowed |
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
| `-upstream.tls.ca` | host trust store | PEM file of the certificates that may sign an `https` upstream's |
| `-server.read-header-timeout` | 10s | how long a client may take to send the headers |
| `-server.read-timeout` | 30s | how long a client may take to send the request |
| `-server.write-timeout` | `-upstream.timeout` + 10s, off where that is off | how long answering may take |
| `-server.idle-timeout` | 2m | how long an idle keep-alive connection is held |
| `-server.shutdown-timeout` | 10s | how long the requests in flight are waited for |

`0` leaves any of the timeouts off. Four of them carry a constraint worth knowing:

- `-server.write-timeout` must stay above `-upstream.timeout`, or the proxy cuts off a response the upstream is still allowed to be sending — and cuts it off as a dropped connection rather than a `504`, the write deadline having passed by the time there is an answer to write. It follows that flag unless you set it, and is off where that flag is off; a value at or below it is rejected at startup.
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
