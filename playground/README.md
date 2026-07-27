# Playground

A GraphQL API with `gqlhash-proxy` in front of it. The API accepts anything, the proxy forwards only the documents in [queries](queries).

```sh
docker compose up --build
```

| | |
| --- | --- |
| http://localhost:8080/graphql | through the proxy |
| http://localhost:4000/graphql | the API, published so the two can be compared |
| http://localhost:8080/healthz | what the proxy has decided so far |
| http://localhost:9090/metrics | Prometheus metrics |

The API holds three users and knows `user`, `users` and `deleteUser`. The allowlist holds `GetUser`, `ListUsers` and `GetUserWithEmail`.

## An allowed document

```sh
curl -s localhost:8080/graphql -H 'Content-Type: application/json' -d '
{"query":"query GetUser($id: ID!) { user(id: $id) { id name } }","variables":{"id":"1"}}'
```

```json
{"data":{"user":{"id":"1","name":"Ada Lovelace"}}}
```

Variables are not part of the document, so the same document works with any of them. Formatting isn't either, so the minified form is the same document:

```sh
curl -s localhost:8080/graphql -H 'Content-Type: application/json' -d '
{"query":"query GetUser($id:ID!){user(id:$id){id name}}","variables":{"id":"3"}}'
```

```json
{"data":{"user":{"id":"3","name":"Grace Hopper"}}}
```

## A document that isn't allowed

One extra field makes a different document:

```sh
curl -s localhost:8080/graphql -H 'Content-Type: application/json' -d '
{"query":"query GetUser($id: ID!) { user(id: $id) { id name email } }","variables":{"id":"1"}}'
```

```json
{"errors":[{"message":"operation not allowed","extensions":{"code":"OPERATION_NOT_ALLOWED"}}]}
```

403, and the API never saw it. The same goes for the mutation:

```sh
curl -s localhost:8080/graphql -H 'Content-Type: application/json' -d '
{"query":"mutation { deleteUser(id: \"1\") }"}'
```

The API accepts that mutation from anyone who reaches it directly, which is the point of the proxy:

```sh
curl -s localhost:4000/graphql -H 'Content-Type: application/json' -d '
{"query":"mutation { deleteUser(id: \"1\") }"}'
```

```json
{"data":{"deleteUser":true}}
```

## Things to try

### The email query is allowed under its own name

[queries/user-with-email.graphql](queries/user-with-email.graphql) asks for the email, and it works:

```sh
curl -s localhost:8080/graphql -H 'Content-Type: application/json' -d '
{"query":"query GetUserWithEmail($id: ID!) { user(id: $id) { id name email } }","variables":{"id":"2"}}'
```

The rejected request above asked for the same fields under the name `GetUser`. An operation name is part of the document, so the two are different entries.

### Adding a document while it runs

The proxy runs with `-watch`, and [queries](queries) is mounted into it. Write a file:

```sh
cat > queries/count-users.graphql <<'EOF'
query CountUsers {
  users {
    id
  }
}
EOF
```

Within `-watch-interval` the log says `allowlist loaded added=1` and the document is allowed. Delete the file and it stops being allowed. A file that doesn't parse is skipped with `skipping a document that doesn't parse` and the rest keeps working.

### Reformatting a document

Reindent a file in [queries](queries), or add a comment to it. The hash doesn't change, the log reports `added=0 removed=0`, and every client keeps working. That's the property that makes an allowlist of documents maintainable.

### Watching the counters

```sh
curl -s localhost:8080/healthz
```

```json
{"documents":3,"loaded_at":"…","allowed":5,"rejected":4,"malformed":0,"upstream_errors":0}
```

```sh
curl -s localhost:9090/metrics | grep gqlhash
```

`gqlhash_proxy_requests_total{decision}` counts the decisions, `gqlhash_proxy_request_duration_seconds` times them. A rejection is answered without reaching the API, so its buckets are far below the forwarded ones.

### Changing what a document means

Stop the stack and restart it with `-ignore=inputs` added to the proxy command in [docker-compose.yml](docker-compose.yml). `ListUsers` then matches whatever `limit` a client asks for, because argument values stop being part of the hash.

`-exact` is the other direction: it compares canonical forms instead of hashes, so no collision can grant access.

## What's here

| | |
| --- | --- |
| [docker-compose.yml](docker-compose.yml) | the two services |
| [queries](queries) | the allowlist, mounted into the proxy |
| [api](api) | the sample GraphQL API, a module of its own so its dependency stays out of gqlhash |
| [Dockerfile](Dockerfile) | builds `gqlhash-proxy` from the working tree, not from a release |

See the [Proxy section of the README](../README.md#proxy) for what the proxy does and why.
