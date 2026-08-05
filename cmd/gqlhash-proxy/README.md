# gqlhash-proxy

Part of [gqlhash](../../README.md), which documents the `gqlhash` CLI and the hashing rules. This file covers the proxy: what it serves, every flag it takes and what a deployment needs to know. [`gqlhash-proxy-fhttp`](../gqlhash-proxy-fhttp/README.md) is the same proxy on fasthttp and behaves as described here. It is **experimental and not for production use**.

`gqlhash-proxy` serves an allowlist of documents in front of a GraphQL API. A request whose document is on the list is forwarded. Every other request is rejected with `403 Forbidden` and never reaches the API.

```sh
gqlhash-proxy \
  -server.listen :8080 \
  -upstream.url http://api:4000/graphql \
  -allowlist ./queries \
  -control.listen 127.0.0.1:9090
```

## Request Handling

`-server.listen` serves the proxy on every path. The request path is never routed on, and `-upstream.url` is the endpoint every forwarded request reaches.

Only `GET` and `POST` are served. Any other method is answered `405` with `Allow: GET, POST`, before the document is read at all. Those are the two methods [GraphQL over HTTP](https://graphql.github.io/graphql-over-http/draft/) defines. Without the check, a `DELETE` carrying an allowed document would reach the API in a shape no allowlist entry describes.

The document is read where that specification puts it: the `query` parameter of a `GET`, or the `query` member of an `application/json` body. The specification is a working draft and defines neither an `application/graphql` body nor batching. The proxy reads both anyway, the first as the document itself and the second under `-server.max-batch`.

## The Upstream URL

`-upstream.url` is used whole, its query included. An API reached as `https://api.example/graphql?env=staging` gets `env=staging` on every forwarded request, ahead of whatever the client sent. The proxy decides only the path and the query of the incoming request: the path is replaced by the endpoint's, and the query is joined onto the endpoint's. Nothing else of the URL is dropped on the way.

Two shapes are refused at startup instead of being taken and ignored:

- **A `query` parameter.** It would reach the API beside the document the client sent. Which of the two the API reads is the API's business, and the allowlist saw only one of them.
- **Credentials**, as in `https://user:pass@api.example/graphql`. Nothing forwards them: only `http.Client` turns the userinfo of a URL into an `Authorization` header, and a reverse proxy builds its request through a transport. Taken, they would reach the log and nothing else. They are also refused for the same reason `GQLHASH_PROXY_CONTROL_TOKEN` has no flag: anyone on the host can read a process argument. An API that wants credentials takes them from a header the client sends.

## Batching

`-server.max-batch` is how many documents one request may carry as a JSON array. Every one of them has to be allowed. The default is `0`, which refuses an array outright.

That default is deliberate. The only other bound is `-server.max-body`, which counts **bytes**. At the default 1 MiB the smallest allowed element (`{"query":"{foo bar}"}`, 22 bytes with its separator) fits **47,662** times. One request could then ask the API for 47,662 executions while `/status` reports `"allowed":1` and any rate limit in front of the proxy sees a single request. Set the cap to what your clients actually batch.

Past it a request is `413` with `BATCH_TOO_LARGE`, counted as `batch_too_large`. The scan stops at the document that breaks the cap, which costs the cap and not the whole megabyte.

Every element has to carry a document of its own, or the request is `400`. An element carrying none is never counted, and `[{"query":"<allowed>"}, 7, 7, …]` would otherwise pass a cap of 1 and reach the API whole. `7`, `{}`, `null` and `{"variables":{}}` are each a refusal, and the scan stops at the first of them.

## The Allowlist

`-allowlist` is a directory of `.graphql` and `.gql` files holding the allowed documents. The proxy hashes them itself, which makes the documents the source of truth. Formatting and comments may differ between a file and what a client sends. The set of definitions may not: one file is one entry.

A `.graphqls` file in the same directory is read as the schema, and every document is then checked against it. One asking for a field the schema doesn't have is skipped like one that doesn't parse. Without such a file nothing is checked. Several `.graphqls` files are read as one schema, and a schema that doesn't parse leaves the documents unchecked rather than unserved.

Two files whose documents hash alike are both skipped. Which one a request meant is unknowable, and allowing the wrong one is worse than allowing neither.

A document that doesn't parse is skipped with an error log, at startup and on reload alike. One broken file then doesn't keep the rest from being served. A directory with no usable document serves an empty allowlist and rejects everything.

## Ambiguous Requests

The proxy rejects ambiguous requests with `400 Bad Request`. Both keys in `{"query":"<allowed>","quer\u0079":"<anything>"}` unescape to `query`. The proxy can't tell which document would reach the API. It hashes neither and refuses. The same goes for a GET naming `query` twice, percent-encoded or not. A `GET` carrying a body is the same case: its document is the query parameter, and a body is a second place one could be.

## Refusals

The proxy writes its own envelope for every refusal and for an upstream failure. It goes out as `application/json; charset=utf-8`, the legacy media type [GraphQL over HTTP](https://graphql.github.io/graphql-over-http/draft/) defines and every client parses. A request whose `Accept` names `application/graphql-response+json` gets that media type instead, with the same envelope. Naming it is what asks for it: `*/*`, `application/*` and no `Accept` header at all state no preference and keep `application/json`. `-opaque-errors` doesn't reach this, because what an answer is written in is no part of why it was refused.

`-opaque-errors` answers every rejection with `403` and `OPERATION_NOT_ALLOWED`. A caller then can't tell a document that isn't on the list from one that's too deep, too large, malformed or ambiguous. It hides **why** a request was refused, not **whether** a document is on the list. Nothing can hide the latter: an allowed document comes back with the API's own answer, which separates the two on any working deployment. What the flag protects is the detail, because a named reason maps the shape of your allowlist and your limits.

It covers the proxy's own rejections only. An upstream that can't be reached is still `502 UPSTREAM_UNAVAILABLE` and a timeout still `504`. Neither is a rejection, and answering them as one would leave a client unable to tell a refused request from a broken API.

## TLS

`-server.tls.cert` and `-server.tls.key` serve the traffic port over HTTPS. The first is a PEM certificate, leaf first and any intermediates after it. The second is its key. Give both or neither: one alone fails at startup, as does a pair that can't be loaded, which keeps the proxy from binding a port it can't serve. Without them the traffic port is plain HTTP, which belongs behind a load balancer terminating TLS itself.

```sh
gqlhash-proxy -upstream.url http://api:4000/graphql -allowlist ./queries \
  -server.tls.cert /etc/ssl/proxy.pem -server.tls.key /etc/ssl/proxy.key
```

Served this way `gqlhash-proxy` offers HTTP/2 over ALPN and falls back to HTTP/1.1. [`gqlhash-proxy-fhttp`](../gqlhash-proxy-fhttp/README.md) is HTTP/1.1 only. The control server is plain HTTP either way and belongs on an address a client of the API can't reach.

The upstream hop is usually plain `http`. Most deployments put the proxy beside the API, and TLS there costs a handshake per connection to protect a wire nobody else is on. Use `https` where the upstream sits across a boundary you don't control.

An `https` upstream has its certificate verified against the host's trust store. `-upstream.tls.ca` names a PEM file to verify it against instead, which is what an upstream behind a private CA needs. It replaces the trust store rather than adding to it. Put several certificates in the one file if you need more than one. It is read at startup, and a file that is missing or holds none stops the proxy there instead of surfacing later as an unreachable upstream.

The hostname is still checked against the certificate, and there is no flag to skip that. Any machine on the path could stand in for a proxy that skipped it, and every allowed document would go to whoever answered.

```sh
gqlhash-proxy -upstream.url https://api.internal/graphql -allowlist ./queries \
  -upstream.tls.ca /etc/ssl/private-ca.pem
```

## The Control Server

`-control.listen 127.0.0.1:9090` serves the control server on that address, which is separate from the port that serves traffic. It provides [Prometheus](https://prometheus.io/) metrics on `/metrics`, a liveness probe on `/healthz`, and rereads the allowlist on `POST /reload`.

`/status` reports the size of the allowlist, when it was loaded, and the counters for every decision and for upstream failures. Each refusal is counted apart: `rejected` for a document that isn't on the list, `malformed` for a request that carries none, `too_large` past `-server.max-body`, `ambiguous` for one naming its document twice, `too_deep` past the depth limit, `batch_too_large` past `-server.max-batch`, and `method_not_allowed` for a method other than `GET` or `POST`. Like the metrics it needs no token and isn't served on the traffic port. It is operational state, not something a client of the API should see.

`/healthz` answers `200 ok` while the proxy serves. It takes `GET` or `HEAD` and no token, since a probe carries no `Authorization` header. It computes nothing, because a proxy that can't load its allowlist fails to start. What a probe reads is the endpoint going away: a shutdown closes the control server first and drains the traffic port afterwards, which takes the pod out of service while it finishes the requests in flight.

A load that skipped files doesn't change the answer. One malformed document would otherwise take every replica out of service. The proxy keeps serving the documents that did load, and `/status` and the metrics report the rest.

**The traffic port belongs entirely to the API.** No health endpoint is served there, and none will be: the proxy forwards on any path, and reserving one would take a path a client may already be posting GraphQL to. Point liveness and readiness at the control port, and use a TCP check against `-server.listen` to cover the traffic listener itself.

`GQLHASH_PROXY_CONTROL_TOKEN` requires `Authorization: Bearer <token>` on `/reload`, compared in constant time. Metrics are served without it. There is no flag for the token: anyone on the host can read a process argument through `ps` or `/proc/<pid>/cmdline`, and the environment of a process is not readable that way.

## Container Image

`ghcr.io/romshark/gqlhash-proxy` carries the proxy for `linux/amd64` and `linux/arm64`. It holds the binary of the release it is tagged with, on [distroless static](https://github.com/GoogleContainerTools/distroless), and runs as an unprivileged user. The image has no shell and no package manager.

```sh
docker run --rm \
  -p 8080:8080 \
  -v "$PWD/queries:/queries:ro" \
  ghcr.io/romshark/gqlhash-proxy:2 \
  -server.listen=:8080 \
  -upstream.url=http://api:4000/graphql \
  -allowlist=/queries \
  -control.listen=:9090
```

Three tags: `2.0.1` is one release and never moves, `2` follows the latest release of that major, and `latest` follows the newest of any. Pin the exact version wherever a rebuild has to produce what the last one did. A prerelease publishes only its own version, and neither `2` nor `latest` follows one.

**`-control.listen` has to be set.** Its default `127.0.0.1:9090` is the container's own loopback, reachable from nothing else, and `/metrics`, `/healthz` and `/reload` answer nobody until it moves to `:9090`. Keep it off `-p` even then: publish the traffic port and leave the control port to the network the scrape and the probe come from.

**The allowlist is a mount, not a layer.** The image holds no documents. Mount the directory read-only and `POST /reload` is what changes it. Baking a layer instead ties every edit to a new image, which is the better trade where the documents ship with the clients that send them. Either way the proxy reads that directory at startup and refuses to start without it.

Every flag has a `GQLHASH_PROXY_*` variable, which a compose file or a pod spec usually uses instead of an argument list:

```yaml
services:
  proxy:
    image: ghcr.io/romshark/gqlhash-proxy:2
    ports:
      - "8080:8080"
    volumes:
      - ./queries:/queries:ro
    environment:
      GQLHASH_PROXY_UPSTREAM_URL: http://api:4000/graphql
      GQLHASH_PROXY_ALLOWLIST: /queries
      GQLHASH_PROXY_CONTROL_LISTEN: ":9090"
      # Never a flag, see above: an argument list is readable through `ps`.
      GQLHASH_PROXY_CONTROL_TOKEN: ${PROXY_CONTROL_TOKEN}
```

A liveness probe points at `/healthz` on the control port, never at the traffic port, which belongs entirely to the API. Give the pod a `terminationGracePeriodSeconds` above `-server.shutdown-timeout`. Below it the kill arrives while the proxy is still draining the requests in flight.

The image runs as uid 65532 and needs no more. It writes nothing and takes `readOnlyRootFilesystem`, `allowPrivilegeEscalation: false` and a dropped capability set as it is. It cannot bind below port 1024: `-server.listen=:80` fails, and a service port maps to `:8080` instead.

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

## Flags

Every flag of the proxy, with its default:

| Flag | Default | |
| --- | --- | --- |
| `-upstream.url` | required | the GraphQL API a request is forwarded to, its query included |
| `-allowlist` | required | the directory the documents are read from |
| `-server.listen` | `:8080` | where the traffic is served |
| `-server.tls.cert` | off | PEM certificate to serve the traffic port over HTTPS with, needs `-server.tls.key` |
| `-server.tls.key` | off | PEM private key for `-server.tls.cert` |
| `-control.listen` | `127.0.0.1:9090` | where `/metrics`, `/status`, `/healthz` and `/reload` are served |
| `-hash` | `sha2` | `sha2`, `sha3`, `blake2b`, `blake2s` or `blake3` |
| `-ignore` | `nothing` | what to leave out of the hash, see [Ignoring Input Values](../../README.md#ignoring-input-values) |
| `-server.max-body` | 1 MiB | largest request body accepted |
| `-depth-limit` | 128 | how deeply a document may nest before it's refused, counted as `too_deep` |
| `-server.max-batch` | 0 (off) | documents a batched request may carry, every one of which has to be allowed |
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

`0` leaves any of the timeouts off, with one exception. `-server.shutdown-timeout` is how long a shutdown *waits*, and `0` there waits for nothing and abandons the requests in flight. A negative duration is refused at startup on every one of them, that one included. It would otherwise mean the same "wait for nothing" nobody meant to ask for.

These carry a constraint worth knowing:

- `-server.write-timeout` must stay above `-upstream.timeout`. Below it the proxy cuts off a response the upstream is still allowed to be sending, and cuts it off as a dropped connection rather than a `504`, the write deadline having passed by the time there is an answer to write. It follows that flag unless you set it, and is off where that flag is off. A value at or below it is rejected at startup.
- `-server.read-timeout` has to fit `-server.max-body` arriving over the slowest link you serve. It must not be below `-server.read-header-timeout`, which would leave that one without effect.
- `-server.idle-timeout` must be above the idle timeout of any load balancer in front. Below it the balancer reuses a connection the proxy is closing.
- `-upstream.max-idle-conns-per-host` is what caps connection reuse. There is one upstream, and every forwarded request draws from that one pool. Keep it at or above the requests you serve at once. Below that the surplus connections are dialed again per request, see [tuning](../../TUNING_GQLHASH_PROXY.md).
- `-upstream.max-conn-lifetime` matters wherever the upstream is several backends behind one name. They are balanced per connection, and a pool that never turns over never reaches a backend added after it filled. A large `-upstream.max-idle-conns-per-host` makes that worse rather than better.
- [`gqlhash-proxy-fhttp`](../gqlhash-proxy-fhttp/README.md) takes the same flags and serves them with fasthttp, forwarding around 59% faster on a third of the memory. It is **experimental and not for production use**. It gives up cancellation on client disconnect, HTTP/2 and a well-worn HTTP parser, and it hands a client's request trailer to your API as an ordinary header. [GQLHASH_PROXY_FHTTP.md](../../GQLHASH_PROXY_FHTTP.md) has the whole trade, measured.

`-hash` defaults to `sha2` as `gqlhash` does, which makes a hash built with either one match. Unlike `gqlhash` it takes only the collision-resistant functions, since collision resistance is an allowlist's security property. `crc32`, `crc64`, `fnv`, `fnv1a` and `xxh64` are collidable by construction, and `md5` and `sha1` are broken.

`-trust-forwarded` appends the peer to the `X-Forwarded-*` headers instead of replacing them. A proxy behind a load balancer needs that for the API to still see the original client. Set it only there. A client that connects directly can otherwise claim any address.

`/metrics` exposes request counters by decision, upstream errors, the allowlist size and load time, a request duration histogram, and the Go runtime collectors. Rejections are counted, not logged, and sit at debug level. A flood of them would otherwise write one line each.

Every flag can be given through the environment instead, as `GQLHASH_PROXY_` followed by its name with the dashes and dots as underscores: `GQLHASH_PROXY_SERVER_MAX_BODY=4096`, `GQLHASH_PROXY_UPSTREAM_URL=http://api:4000/graphql`. A flag on the command line wins. `gqlhash` reads no environment at all, which keeps a variable from quietly changing the hashes a pipeline produces.

[TUNING_GQLHASH_PROXY.md](../../TUNING_GQLHASH_PROXY.md) has the throughput and latency of both paths, what moves the forwarded one, and how to measure it without measuring the load generator instead. `go run ./internal/cmd/loadtest` runs it.

[playground](../../playground) runs the proxy in front of a sample GraphQL API with `docker compose up --build`, with a few allowed documents and the schema they're checked against.

## File Uploads and APQ

GraphQL clients commonly send two request shapes that carry no document for the proxy to hash. Both are answered `400` and neither reaches the API:

- **`multipart/form-data`**, the [GraphQL multipart request spec](https://github.com/jaydenseric/graphql-multipart-request-spec) used for file uploads. The document is a field inside the multipart body rather than the body itself.
- **An APQ request carrying only `extensions.persistedQuery`**, with no `query` member. Automatic persisted queries are the client asking the *API* to look a hash up. This proxy hashes documents itself, and such a request holds nothing to hash.

Neither is malformed. Both are valid requests that simply carry no document, and without one there is nothing to check against the allowlist. `TestShapesRealClientsSendThatAreRefused` pins them.

## Protocol Upgrades

`Upgrade` and the `Connection` token naming it are dropped on the way upstream, and a `101` from the API becomes a `502` rather than a relayed switch. A tunnel is a channel the proxy can hash once and never again, which is the one thing an allowlist in front of an API can't allow. `Upgrade: h2c`, which `curl --http2` offers in cleartext, is dropped the same way and the request is served over HTTP/1.1.

Subscriptions over **WebSocket** therefore can't reach an API through this proxy. Subscriptions over **SSE** can, see [Subscriptions](#subscriptions).

## Subscriptions

Use [GraphQL over SSE](https://github.com/enisdenjo/graphql-sse) in **distinct connections mode**, which is what [GraphQL Yoga](https://the-guild.dev/graphql/yoga-server/docs/features/subscriptions) defaults to. Every operation is an ordinary `POST` or `GET` carrying an ordinary document, hashed and checked against the allowlist exactly like a query. Put the subscription document in the allowlist directory and it is served.

The answer is relayed event by event for as long as the API sends events. `-upstream.timeout` and `-server.write-timeout` bound an exchange, and a stream is not one, which is why neither cuts a subscription short. The timeout still bounds the wait for the answer's headers.

```sh
curl -N -H 'Accept: text/event-stream' -H 'Content-Type: application/json' \
  -d '{"query":"subscription { ticks }"}' http://localhost:8080/graphql
```

**Single connection mode is not supported.** Its `PUT` reservation, its stream-opening `GET` and its `DELETE` carry no GraphQL document, and forwarding a request the allowlist never saw is the one thing this proxy is for. The `PUT` and the `DELETE` are answered `405`, the `GET` `400`.

**WebSocket** (`graphql-ws`) can't work here at all. After the upgrade the frames are opaque, and one allowlisted handshake would open an unhashed channel. Route it around the proxy at your ingress if you need it.
