import { check } from 'k6';

import { runRaceLifecycle } from '../lib/ws-client.js';

const RACE_ID = __ENV.RACE_ID;
const SESSION_TOKEN_1 = __ENV.SESSION_TOKEN_1;
const SESSION_TOKEN_2 = __ENV.SESSION_TOKEN_2;
const BASE_URL_1 = __ENV.BASE_URL_1;
const BASE_URL_2 = __ENV.BASE_URL_2;
const DISTANCE_METERS = parseInt(__ENV.DISTANCE_METERS || '10', 10);

if (!RACE_ID || !SESSION_TOKEN_1 || !SESSION_TOKEN_2 || !BASE_URL_1 || !BASE_URL_2) {
  throw new Error(
    'RACE_ID, SESSION_TOKEN_1, SESSION_TOKEN_2, BASE_URL_1, and BASE_URL_2 are all required (set by load/multi-instance-check.sh)',
  );
}

const SESSION_TOKENS = [SESSION_TOKEN_1, SESSION_TOKEN_2];
// BASE_URLS[i] is SESSION_TOKENS[i]'s own gateway — the two participants of
// this race are deliberately connected through two different ws-gateway
// instances (multi-instance-dev-setup.md's whole point: prove the bus
// carries state correctly between gateways, not just within one process).
const BASE_URLS = [BASE_URL_1, BASE_URL_2];

export const options = {
  scenarios: {
    multi_instance_check: {
      executor: 'per-vu-iterations',
      vus: 2,
      iterations: 1,
      maxDuration: '2m',
    },
  },
  thresholds: {
    checks: ['rate==1.0'],
  },
};

// This script deliberately skips race-lifecycle.js's own setup()-driven
// REST orchestration — load/multi-instance-check.sh already registered,
// created, and joined this race via curl, since it needs those real HTTP
// responses itself (race_id, session_token) for its own redis-cli/log-grep
// ownership assertions (multi-instance-dev-setup.md's verification plan,
// steps 1-4). This script's only job is the two WebSocket connections, each
// run through its own participant's own gateway (BASE_URL_1/BASE_URL_2) to
// prove the cross-gateway, cross-instance bus relay — not the REST creation
// flow, which race-lifecycle.js's setup() already covers for the
// single-instance case.
export default function () {
  const sessionToken = SESSION_TOKENS[__VU - 1];
  const baseURL = BASE_URLS[__VU - 1];
  let sawRaceStarted = false;
  let sawRaceFinished = false;

  runRaceLifecycle(baseURL, RACE_ID, sessionToken, DISTANCE_METERS, function (msg) {
    if (msg.type === 'race_started') {
      sawRaceStarted = true;
    }
    if (msg.type === 'race_finished') {
      sawRaceFinished = true;
      check(msg, {
        'race_finished has results': (m) => Array.isArray(m.results) && m.results.length > 0,
      });
    }
  });

  check(null, {
    'received race_started (verification step 5/6)': () => sawRaceStarted,
    'received race_finished (verification step 7/8)': () => sawRaceFinished,
  });
}
