#!/usr/bin/env bash
# Load tests gqlhash-proxy end to end: an upstream API, the proxy in front of
# it, and an HTTP load generator against the proxy.
#
#	scripts/loadtest.sh [generator] [duration]
#
# generator is oha, vegeta, k6, h2load or wrk. Without one it takes the first
# that's installed, preferring the ones whose own overhead is lowest.
#
# The generator has to be faster than what it measures, so check that it isn't
# the bottleneck before reading anything into the numbers. Measured on an Apple
# M4 Pro, k6 and h2load land within 10% of each other against this proxy
# (~129k req/s rejected, ~43k req/s forwarded), which means the proxy is the
# limit and either tool is fine. On faster hardware, or against a target that
# answers in microseconds, prefer oha or vegeta: vegeta drives a constant
# arrival rate, which is what latency percentiles need.
#
# Both tools count a 403 as a failed request. On the rejected run that is the
# expected answer, not an error.
set -euo pipefail

GENERATOR="${1:-}"
DURATION="${2:-10s}"
UPSTREAM_ADDR="127.0.0.1:14001"
PROXY_ADDR="127.0.0.1:14002"
TARGET="http://${PROXY_ADDR}/graphql"

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WORK="$(mktemp -d)"
trap 'kill $(jobs -p) 2>/dev/null || true; rm -rf "$WORK"' EXIT

if [ -z "$GENERATOR" ]; then
	for candidate in oha vegeta wrk h2load k6; do
		if command -v "$candidate" >/dev/null 2>&1; then
			GENERATOR="$candidate"
			break
		fi
	done
fi
if [ -z "$GENERATOR" ]; then
	echo "no load generator found, install one of: oha vegeta wrk h2load k6" >&2
	exit 1
fi

# The allowlist holds the one document the load test sends.
mkdir -p "$WORK/queries"
cat > "$WORK/queries/get-user.graphql" <<'EOF'
query GetUser {
  user(id: 1) {
    name
    email
  }
}
EOF

ALLOWED='{"operationName":"GetUser","query":"query GetUser{user(id:1){name email}}","variables":{"id":1}}'
REJECTED='{"operationName":"GetUser","query":"query GetUser{user(id:1){name secret}}","variables":{"id":1}}'
printf '%s' "$ALLOWED" > "$WORK/allowed.json"
printf '%s' "$REJECTED" > "$WORK/rejected.json"

echo "building"
go build -o "$WORK/gqlhash-proxy" "$ROOT/cmd/gqlhash-proxy"

echo "starting the upstream on $UPSTREAM_ADDR"
go run "$ROOT/scripts/loadtest_upstream.go" -server.listen "$UPSTREAM_ADDR" \
	> "$WORK/upstream.log" 2>&1 &

echo "starting the proxy on $PROXY_ADDR"
"$WORK/gqlhash-proxy" \
	-server.listen "$PROXY_ADDR" \
	-upstream.url "http://${UPSTREAM_ADDR}/graphql" \
	-allowlist "$WORK/queries" \
	-control.listen "127.0.0.1:14003" \
	-log.level warn \
	> "$WORK/proxy.log" 2>&1 &

# Wait for both to answer before measuring anything.
for _ in $(seq 1 100); do
	if curl -fsS -o /dev/null -X POST "$TARGET" \
		-H 'Content-Type: application/json' -d "$ALLOWED" 2>/dev/null; then
		break
	fi
	sleep 0.1
done
code=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$TARGET" \
	-H 'Content-Type: application/json' -d "$ALLOWED")
if [ "$code" != "200" ]; then
	echo "the proxy doesn't forward the allowed document (got $code)" >&2
	cat "$WORK/proxy.log" >&2
	exit 1
fi
code=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$TARGET" \
	-H 'Content-Type: application/json' -d "$REJECTED")
if [ "$code" != "403" ]; then
	echo "the proxy doesn't reject the unknown document (got $code)" >&2
	exit 1
fi

run() { # run <label> <body-file> <expected-status>
	local label="$1" body="$2" status="$3"
	echo
	echo "=== $label through $GENERATOR for $DURATION"
	case "$GENERATOR" in
	oha)
		oha -z "$DURATION" -c 50 --no-tui -m POST \
			-H 'Content-Type: application/json' -D "$body" "$TARGET"
		;;
	vegeta)
		printf 'POST %s\nContent-Type: application/json\n@%s\n' \
			"$TARGET" "$body" |
			vegeta attack -duration "$DURATION" -rate 0 -max-workers 50 |
			vegeta report
		;;
	wrk)
		# wrk needs the body in a Lua script.
		cat > "$WORK/wrk.lua" <<-EOF
			wrk.method = "POST"
			wrk.headers["Content-Type"] = "application/json"
			wrk.body = [==[$(cat "$body")]==]
		EOF
		wrk -t4 -c50 -d"$DURATION" -s "$WORK/wrk.lua" --latency "$TARGET"
		;;
	h2load)
		h2load --h1 -c50 -t4 -D "${DURATION%s}" -m1 \
			-H 'Content-Type: application/json' -d "$body" "$TARGET"
		;;
	k6)
		DURATION="$DURATION" TARGET="$TARGET" k6 run \
			-e "TARGET=$TARGET" -e "DURATION=$DURATION" \
			"$ROOT/scripts/loadtest.js"
		return
		;;
	esac
	echo "(expected status $status)"
}

if [ "$GENERATOR" = "k6" ]; then
	# The k6 script drives both paths itself.
	run "allowed and rejected" "$WORK/allowed.json" 200
else
	run "allowed, forwarded upstream" "$WORK/allowed.json" 200
	run "rejected, answered by the proxy" "$WORK/rejected.json" 403
fi

echo
echo "=== proxy log, last lines"
tail -5 "$WORK/proxy.log" || true
