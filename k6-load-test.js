// k6 load test for the rate-limiter service.
//
// Run a single scenario by name via SCENARIO env var:
//   SCENARIO=sustained|burst|mixed_algos
//
// BASE_URL defaults to http://localhost:8080 (or pass via -e BASE_URL=...).
//
// We tag each request with `algo` so the summary breaks down latency per
// algorithm. 429 responses are first-class results (rate-limit enforcement
// working), so we tell k6 NOT to count them as HTTP failures — otherwise
// http_req_failed conflates "real errors" with "rate limiter doing its job".

import http from 'k6/http';
import { check } from 'k6';
import { setResponseCallback, expectedStatuses } from 'k6/http';
import { Rate } from 'k6/metrics';

setResponseCallback(expectedStatuses({ min: 200, max: 299 }, 429));

const BASE_URL = __ENV.BASE_URL || 'http://localhost:8080';
const SCENARIO = __ENV.SCENARIO || 'sustained';

const rejections = new Rate('rate_limit_rejections');

const allScenarios = {
  // Ramp to 1000 req/s, hold 30s, ramp down. Steady-throughput test.
  sustained: {
    executor: 'ramping-arrival-rate',
    startRate: 100,
    timeUnit: '1s',
    preAllocatedVUs: 200,
    maxVUs: 2000,
    stages: [
      { duration: '10s', target: 1000 },
      { duration: '30s', target: 1000 },
      { duration: '5s',  target: 0    },
    ],
    exec: 'sustainedExec',
  },

  // 5000 req/s flat for 10s. Spike-handling test.
  burst: {
    executor: 'constant-arrival-rate',
    rate: 5000,
    timeUnit: '1s',
    duration: '10s',
    preAllocatedVUs: 500,
    maxVUs: 5000,
    exec: 'burstExec',
  },

  // 600 req/s split across all three algorithms for 30s.
  mixed_algos: {
    executor: 'constant-arrival-rate',
    rate: 600,
    timeUnit: '1s',
    duration: '30s',
    preAllocatedVUs: 200,
    maxVUs: 2000,
    exec: 'mixedExec',
  },
};

export const options = {
  scenarios: { [SCENARIO]: allScenarios[SCENARIO] },
  thresholds: {
    'http_req_duration':              ['p(95)<50'],
    'http_req_failed':                ['rate<0.01'],
    'http_req_duration{algo:fixed}':   ['p(95)<50'],
    'http_req_duration{algo:sliding}': ['p(95)<50'],
    'http_req_duration{algo:token}':   ['p(95)<50'],
  },
  // Threshold failures should not abort the run — we want the full result set
  // to document honestly even when the laptop can't keep up.
  noConnectionReuse: false,
  summaryTrendStats: ['avg', 'min', 'med', 'p(90)', 'p(95)', 'p(99)', 'max'],
};

function hitCheck(algo) {
  // Vary the key per virtual user so we exercise concurrency across many
  // buckets, not a single hot key. A suffix per algorithm keeps per-algo
  // state independent in mixed_algos.
  const baseKey = `load-test-${__VU}`;
  let url;
  if (algo === 'token') {
    url = `${BASE_URL}/check?key=${baseKey}-tok&algorithm=token&capacity=100&refill=10`;
  } else if (algo === 'sliding') {
    url = `${BASE_URL}/check?key=${baseKey}-sld&algorithm=sliding&limit=100&window=60`;
  } else {
    url = `${BASE_URL}/check?key=${baseKey}-fxd&algorithm=fixed&limit=100&window=60`;
  }
  const res = http.post(url, null, { tags: { algo } });
  check(res, {
    'status is 200 or 429': r => r.status === 200 || r.status === 429,
  });
  rejections.add(res.status === 429);
}

export function sustainedExec() { hitCheck('fixed'); }
export function burstExec()     { hitCheck('fixed'); }

export function mixedExec() {
  const algos = ['fixed', 'sliding', 'token'];
  hitCheck(algos[(__VU + __ITER) % 3]);
}
