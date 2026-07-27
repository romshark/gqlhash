// k6 script for the proxy. Run it through scripts/loadtest.sh, which starts the
// upstream and the proxy first:
//
//	k6 run -e TARGET=http://127.0.0.1:14002/graphql scripts/loadtest.js
//
// It drives the two paths that matter: an allowed document, which is forwarded,
// and one that isn't on the allowlist, which the proxy answers alone.
import http from 'k6/http'
import { check } from 'k6'

const TARGET = __ENV.TARGET || 'http://127.0.0.1:14002/graphql'

const ALLOWED = JSON.stringify({
  operationName: 'GetUser',
  query: 'query GetUser{user(id:1){name email}}',
  variables: { id: 1 },
})

const REJECTED = JSON.stringify({
  operationName: 'GetUser',
  query: 'query GetUser{user(id:1){name secret}}',
  variables: { id: 1 },
})

const params = { headers: { 'Content-Type': 'application/json' } }

export const options = {
  scenarios: {
    allowed: {
      executor: 'constant-vus',
      vus: Number(__ENV.VUS || 50),
      duration: __ENV.DURATION || '10s',
      exec: 'allowed',
    },
    rejected: {
      executor: 'constant-vus',
      vus: Number(__ENV.VUS || 50),
      duration: __ENV.DURATION || '10s',
      exec: 'rejected',
      startTime: __ENV.DURATION || '10s',
    },
  },
  thresholds: {
    // A forwarded request stays well under a tenth of a second, and a rejection
    // is answered without the upstream hop.
    'http_req_duration{scenario:allowed}': ['p(99)<100'],
    'http_req_duration{scenario:rejected}': ['p(99)<50'],
    checks: ['rate==1.0'],
  },
}

export function allowed() {
  const res = http.post(TARGET, ALLOWED, params)
  check(res, { 'allowed is 200': (r) => r.status === 200 })
}

export function rejected() {
  const res = http.post(TARGET, REJECTED, params)
  check(res, { 'rejected is 403': (r) => r.status === 403 })
}
