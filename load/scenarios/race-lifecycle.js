import { check } from 'k6';
import http from 'k6/http';

import { registerAndLogin, authHeaders } from '../lib/auth.js';
import { runRaceLifecycle } from '../lib/ws-client.js';

const BASE_URL = __ENV.BASE_URL || 'http://localhost:8080';
const NUM_RACES = parseInt(__ENV.NUM_RACES || '5', 10);
const VUS_PER_RACE = parseInt(__ENV.VUS_PER_RACE || '8', 10);
const DISTANCE_METERS = parseInt(__ENV.DISTANCE_METERS || '30', 10);

// Mirrors internal/race's MaxParticipants constant (internal/race/handler.go)
// — a real race can never hold more than this, so neither can a simulated one.
const MAX_PARTICIPANTS = 10;

if (VUS_PER_RACE > MAX_PARTICIPANTS) {
  throw new Error(`VUS_PER_RACE (${VUS_PER_RACE}) exceeds race.MaxParticipants (${MAX_PARTICIPANTS})`);
}

const TOTAL_VUS = NUM_RACES * VUS_PER_RACE;

// RUN_ID disambiguates this run's registered emails from any previous run's
// — users aren't cleaned up automatically, so re-running the script must
// not collide with already-registered addresses from an earlier run.
const RUN_ID = Date.now();

export const options = {
  scenarios: {
    race_lifecycle: {
      executor: 'per-vu-iterations',
      vus: TOTAL_VUS,
      iterations: 1,
      maxDuration: '5m',
    },
  },
};

// setup() runs once, single-threaded, before any VU executes — the only
// place k6 can coordinate "who creates a race, who joins it, when it
// starts" at all, since VUs have no cross-VU communication at runtime (no
// shared memory, no messaging, short of standing up an external message
// bus, which is Phase 4 territory this project doesn't have yet). Every
// REST call below is the real endpoint, not mocked — it just runs
// sequentially here instead of "per VU," which was never really
// achievable for resource-creation steps like this in k6 anyway. The
// genuinely-concurrent, load-generating part — every VU's real WebSocket
// connection and telemetry stream — still happens fully in parallel, in
// the default function below.
export function setup() {
  // assignments[i] is VU (i+1)'s {raceID, sessionToken} — k6 VUs are
  // 1-indexed via __VU.
  const assignments = [];

  for (let r = 0; r < NUM_RACES; r++) {
    const creatorEmail = `loadtest-r${r}-creator-${RUN_ID}@example.com`;
    const creatorToken = registerAndLogin(creatorEmail, 'loadtest-password-1', `LoadTest Creator ${r}`);

    const createRes = http.post(
      `${BASE_URL}/races`,
      JSON.stringify({ name: `Load Test Race ${r}`, distance_meters: DISTANCE_METERS }),
      authHeaders(creatorToken),
    );
    check(createRes, { 'race created (201)': (res) => res.status === 201 });
    const race = createRes.json();
    const raceID = race.id;

    // CreateRace auto-joins the creator as a participant (race-screen.md)
    // and returns their own session_token in the same response — no
    // separate join call needed for VU 0 of this race.
    assignments.push({ raceID, sessionToken: race.session_token });

    for (let p = 1; p < VUS_PER_RACE; p++) {
      const email = `loadtest-r${r}-p${p}-${RUN_ID}@example.com`;
      const token = registerAndLogin(email, 'loadtest-password-1', `LoadTest Player ${r}-${p}`);

      const joinRes = http.post(`${BASE_URL}/races/${raceID}/join`, null, authHeaders(token));
      check(joinRes, { 'joined race (200)': (res) => res.status === 200 });
      assignments.push({ raceID, sessionToken: joinRes.json('session_token') });
    }

    // Every VU assigned to this race has already joined (sequentially,
    // above) by the time start is called — not a race condition against
    // real concurrent joins, a guarantee, since setup() is single-threaded.
    const startRes = http.post(`${BASE_URL}/races/${raceID}/start`, null, authHeaders(creatorToken));
    check(startRes, { 'race started (200)': (res) => res.status === 200 });
  }

  return { assignments };
}

// Runs fully in parallel across every VU — the actual load-generating
// part of this scenario: real WebSocket connections and telemetry streams
// stressing the room actor's single-writer channel/goroutine machinery
// (internal/room, internal/ws) with genuinely concurrent traffic, not
// go test -race's handful of goroutines per test.
export default function (data) {
  const assignment = data.assignments[__VU - 1];
  if (!assignment) {
    throw new Error(`no race assignment for VU ${__VU} (only ${data.assignments.length} provisioned in setup())`);
  }

  runRaceLifecycle(assignment.raceID, assignment.sessionToken, DISTANCE_METERS, function (msg) {
    check(msg, {
      'known message type': (m) =>
        ['race_state', 'race_started', 'race_finished', 'race_expired'].includes(m.type),
    });
  });
}
