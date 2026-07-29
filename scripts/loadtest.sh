#!/usr/bin/env bash
# Load tests gqlhash-proxy end to end: an upstream API, the proxy in front of
# it, and an HTTP load generator against the proxy.
#
#	scripts/loadtest.sh [generator] [duration] [connections]
#
# generator is wrk, oha, h2load or vegeta, the first installed by default. What
# a generator spends per request is CPU the proxy doesn't get: on 24 cores wrk
# costs 6.2us and leaves the proxy 13.8 of them, k6 cost 61.5us, left 5.6, and
# reported 244k req/s where wrk reported 608k. Hence no k6, and a second machine
# for anything published.
#
# oha and vegeta drive a constant arrival rate, which latency percentiles need:
# a closed-loop generator stops sending while the proxy is stalled and never
# records the stall.
#
# The proxy is started with its upstream pool sized to the connections, which
# isn't the default. See GQLHASH_PROXY_TUNING.md.
#
# Every generator counts the 403 of the rejected run as a failure. It's the
# expected answer.
set -euo pipefail

GENERATOR="${1:-}"
DURATION="${2:-10s}"
# 50 connections cannot exceed ~50k req/s against a forward that takes a
# millisecond, which is the connection count and not the proxy. 200 finds the
# ceiling of both paths without the queueing that thousands would add.
CONNECTIONS="${3:-200}"
UPSTREAM_ADDR="127.0.0.1:14001"
PROXY_ADDR="127.0.0.1:14002"
TARGET="http://${PROXY_ADDR}/graphql"

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WORK="$(mktemp -d)"
trap 'kill $(jobs -p) 2>/dev/null || true; rm -rf "$WORK"' EXIT

if [ -z "$GENERATOR" ]; then
	for candidate in wrk oha h2load vegeta; do
		if command -v "$candidate" >/dev/null 2>&1; then
			GENERATOR="$candidate"
			break
		fi
	done
fi
if [ -z "$GENERATOR" ]; then
	echo "no load generator found, install one of: wrk oha h2load vegeta" >&2
	exit 1
fi
# Without this a name the case below doesn't know drives no load at all and
# still leaves for a pass, which reads like a run that measured nothing.
case "$GENERATOR" in
wrk | oha | h2load | vegeta) ;;
*)
	echo "unknown generator: $GENERATOR, expected wrk, oha, h2load or vegeta" >&2
	exit 1
	;;
esac
if ! command -v "$GENERATOR" >/dev/null 2>&1; then
	echo "$GENERATOR isn't installed" >&2
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
go run "$ROOT/scripts/loadtest_upstream.go" -listen "$UPSTREAM_ADDR" \
	> "$WORK/upstream.log" 2>&1 &

echo "starting the proxy on $PROXY_ADDR"
"$WORK/gqlhash-proxy" \
	-server.listen "$PROXY_ADDR" \
	-upstream.url "http://${UPSTREAM_ADDR}/graphql" \
	-allowlist "$WORK/queries" \
	-control.listen "127.0.0.1:14003" \
	-log.level warn \
	-upstream.max-idle-conns-per-host "$CONNECTIONS" \
	-upstream.max-idle-conns "$((CONNECTIONS * 4))" \
	> "$WORK/proxy.log" 2>&1 &
PROXY_PID=$!

# cpu_seconds reports what a process has spent on a CPU so far. procfs where
# there is one, ps where there isn't, which covers macOS at a second's
# resolution — coarse, but the runs are long enough for that not to matter.
cpu_seconds() {
	if [ -r "/proc/$1/stat" ]; then
		awk -v tck="$(getconf CLK_TCK)" '{print ($14 + $15) / tck}' "/proc/$1/stat"
	else
		ps -p "$1" -o time= 2>/dev/null |
			awk -F: '{s=$NF; if (NF>1) s+=$(NF-1)*60; if (NF>2) s+=$(NF-2)*3600; print s}'
	fi
}

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
	echo "=== $label through $GENERATOR for $DURATION, $CONNECTIONS connections"
	local cpu0 elapsed0
	cpu0="$(cpu_seconds "$PROXY_PID")"
	elapsed0=$SECONDS
	case "$GENERATOR" in
	oha)
		oha -z "$DURATION" -c "$CONNECTIONS" --no-tui -m POST \
			-H 'Content-Type: application/json' -D "$body" "$TARGET"
		;;
	vegeta)
		printf 'POST %s\nContent-Type: application/json\n@%s\n' \
			"$TARGET" "$body" |
			vegeta attack -duration "$DURATION" -rate 0 -max-workers "$CONNECTIONS" |
			vegeta report
		;;
	wrk)
		# wrk needs the body in a Lua script. Four threads drive both paths of
		# this proxy, and every thread past that is one the proxy doesn't get.
		cat > "$WORK/wrk.lua" <<-EOF
			wrk.method = "POST"
			wrk.headers["Content-Type"] = "application/json"
			wrk.body = [==[$(cat "$body")]==]
		EOF
		wrk -t4 -c"$CONNECTIONS" -d"$DURATION" -s "$WORK/wrk.lua" --latency "$TARGET"
		;;
	h2load)
		h2load --h1 -c"$CONNECTIONS" -t4 -D "${DURATION%s}" -m1 \
			-H 'Content-Type: application/json' -d "$body" "$TARGET"
		;;
	esac
	# What the proxy spent holding that rate. Divide it by the req/s above for
	# the CPU one request costs, which is the number that ports across machines.
	local elapsed=$((SECONDS - elapsed0))
	[ "$elapsed" -gt 0 ] || elapsed=1
	awk -v a="$cpu0" -v b="$(cpu_seconds "$PROXY_PID")" -v s="$elapsed" \
		'BEGIN { printf "proxy: %.1fs of CPU over %ds, %.1f cores\n", b - a, s, (b - a) / s }'
	echo "(expected status $status)"
}

run "allowed, forwarded upstream" "$WORK/allowed.json" 200
run "rejected, answered by the proxy" "$WORK/rejected.json" 403

echo
echo "=== proxy log, last lines"
tail -5 "$WORK/proxy.log" || true
