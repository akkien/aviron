import ws from 'k6/ws';
import { sleep } from 'k6';

// Mirrors frontend/src/hooks/useRaceSocket.ts's exact bounded reconnect
// shape (reconnect-ui.md) — a few attempts a couple seconds apart, no
// exponential backoff, no production-grade retry strategy. Used only by
// load/multi-instance-check.sh's step 10 (the owning instance killed
// mid-race) to prove the router/registry's own behavior under a
// reconnect-shaped retry loop — this does not exercise the actual React
// frontend code (no browser automation is available in this environment).
const RECONNECT_MAX_ATTEMPTS = 3;
const RECONNECT_DELAY_MS = 2000;

// HANDSHAKE_TIMEOUT_MS bounds a single connect attempt so a hung dial
// (rather than a clean refusal) still resolves within a few seconds.
const HANDSHAKE_TIMEOUT_MS = 3000;

// attemptReconnect tries to open GET /ws (through whatever baseURL points
// at — normally a specific ws-gateway instance) up to
// RECONNECT_MAX_ATTEMPTS times, RECONNECT_DELAY_MS apart, exactly like a
// real client would after its connection drops. Returns true the moment any
// attempt actually opens the WebSocket, false if every attempt fails to
// open.
export function attemptReconnect(baseURL, raceID, sessionToken) {
  const wsURL = baseURL.replace(/^http/, 'ws');
  const url = `${wsURL}/ws?race_id=${encodeURIComponent(raceID)}&session_token=${encodeURIComponent(sessionToken)}`;

  for (let attempt = 1; attempt <= RECONNECT_MAX_ATTEMPTS; attempt++) {
    let opened = false;

    ws.connect(url, {}, function (socket) {
      socket.on('open', function () {
        opened = true;
        socket.close();
      });
      socket.setTimeout(function () {
        socket.close();
      }, HANDSHAKE_TIMEOUT_MS);
    });

    if (opened) {
      return true;
    }

    if (attempt < RECONNECT_MAX_ATTEMPTS) {
      sleep(RECONNECT_DELAY_MS / 1000);
    }
  }

  return false;
}
