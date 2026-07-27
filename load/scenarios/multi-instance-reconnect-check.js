import { check } from 'k6';

import { attemptReconnect } from '../lib/reconnect-client.js';

const RACE_ID = __ENV.RACE_ID;
const SESSION_TOKEN = __ENV.SESSION_TOKEN;
const BASE_URL = __ENV.BASE_URL;

if (!RACE_ID || !SESSION_TOKEN || !BASE_URL) {
  throw new Error(
    'RACE_ID, SESSION_TOKEN, and BASE_URL are required (set by load/multi-instance-check.sh, after killing the owning instance)',
  );
}

export const options = {
  scenarios: {
    reconnect_check: {
      executor: 'shared-iterations',
      vus: 1,
      iterations: 1,
      maxDuration: '30s',
    },
  },
};

// Verification step 10: confirms the *documented* gap
// (race-router.md's Notes — an owning instance dying mid-race is
// unrecoverable) behaves exactly as predicted once the owning instance has
// already been killed: a bounded reconnect attempt exhausts and surfaces
// as a definitive failure, not a hang. A passing check here means the
// failure mode is exactly what's already written down, not that anything
// is now fixed.
export default function () {
  const reconnected = attemptReconnect(BASE_URL, RACE_ID, SESSION_TOKEN);
  check(reconnected, {
    'reconnect eventually fails after the owning instance dies (documented gap)': (r) => r === false,
  });
}
