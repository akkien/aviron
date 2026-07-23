import ws from 'k6/ws';

const BASE_URL = __ENV.BASE_URL || 'http://localhost:8080';
const WS_URL = BASE_URL.replace(/^http/, 'ws');

// TELEMETRY_MIN_DELAY_MS/TELEMETRY_MAX_DELAY_MS bound realistic typing
// cadence per project-overview.md §4.2 — telemetry is sent one word at a
// time, spaced 0.4-2s apart, never machine-gunned. Unrealistically fast
// telemetry doesn't represent real load and would trip up LastSeq-based
// ordering (internal/room/room.go) in ways a real client never would.
const TELEMETRY_MIN_DELAY_MS = 400;
const TELEMETRY_MAX_DELAY_MS = 2000;

// HUNG_CONNECTION_TIMEOUT_MS is a safety net: one VU that never receives
// race_finished (a lost message, a stalled connection) must not block the
// whole run indefinitely.
const HUNG_CONNECTION_TIMEOUT_MS = 120000;

// paceWatt mirrors frontend/src/components/race-screen/TypingBox.tsx's
// exact pace_watt formula — a cumulative words-per-minute average measured
// from the first word sent, not an instantaneous rate.
function paceWatt(wordsCompleted, startedAtMs) {
  const elapsedMinutes = (Date.now() - startedAtMs) / 60000;
  return elapsedMinutes > 0 ? Math.round(wordsCompleted / elapsedMinutes) : 0;
}

// runRaceLifecycle opens the real GET /ws handshake, sends join_race, then
// streams telemetry messages (one per simulated correctly-typed word) until
// distanceMeters is reached or the room broadcasts race_finished/
// race_expired, whichever comes first. onEvent (optional) is called with
// every decoded server message, for the caller to run checks against.
export function runRaceLifecycle(raceID, sessionToken, distanceMeters, onEvent) {
  const url = `${WS_URL}/ws?race_id=${encodeURIComponent(raceID)}&session_token=${encodeURIComponent(sessionToken)}`;

  return ws.connect(url, {}, function (socket) {
    let seq = 0;
    let wordsCompleted = 0;
    let startedAtMs = null;
    let done = false;

    socket.setTimeout(function () {
      if (!done) {
        done = true;
        socket.close();
      }
    }, HUNG_CONNECTION_TIMEOUT_MS);

    function sendNextWord() {
      if (done || wordsCompleted >= distanceMeters) {
        return;
      }
      if (startedAtMs === null) {
        startedAtMs = Date.now();
      }

      wordsCompleted += 1;
      seq += 1;
      socket.send(
        JSON.stringify({
          type: 'telemetry',
          seq,
          distance_m: wordsCompleted,
          pace_watt: paceWatt(wordsCompleted, startedAtMs),
          ts: Date.now(),
        }),
      );

      if (wordsCompleted < distanceMeters) {
        const delayMs = TELEMETRY_MIN_DELAY_MS + Math.random() * (TELEMETRY_MAX_DELAY_MS - TELEMETRY_MIN_DELAY_MS);
        socket.setTimeout(sendNextWord, delayMs);
      }
    }

    socket.on('open', function () {
      socket.send(JSON.stringify({ type: 'join_race', race_id: raceID }));
      sendNextWord();
    });

    socket.on('message', function (data) {
      const msg = JSON.parse(data);
      if (onEvent) {
        onEvent(msg);
      }
      if (msg.type === 'race_finished' || msg.type === 'race_expired') {
        done = true;
        socket.close();
      }
    });

    socket.on('error', function () {
      done = true;
    });
  });
}
