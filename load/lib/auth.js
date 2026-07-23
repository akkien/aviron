import http from 'k6/http';

const BASE_URL = __ENV.BASE_URL || 'http://localhost:8080';

// registerAndLogin mirrors frontend/src/lib/auth.ts's real flow — register,
// then log in to get a JWT — rather than a shortcut that skips real auth.
// Returns the signed JWT (loginResponse.token).
export function registerAndLogin(email, password, displayName) {
  const registerRes = http.post(
    `${BASE_URL}/auth/register`,
    JSON.stringify({ email, password, display_name: displayName }),
    { headers: { 'Content-Type': 'application/json' } },
  );
  if (registerRes.status !== 201) {
    throw new Error(`register ${email} failed: ${registerRes.status} ${registerRes.body}`);
  }

  const loginRes = http.post(
    `${BASE_URL}/auth/login`,
    JSON.stringify({ email, password }),
    { headers: { 'Content-Type': 'application/json' } },
  );
  if (loginRes.status !== 200) {
    throw new Error(`login ${email} failed: ${loginRes.status} ${loginRes.body}`);
  }

  return loginRes.json('token');
}

// authHeaders builds the params object for an authenticated REST call.
export function authHeaders(token) {
  return {
    headers: {
      'Content-Type': 'application/json',
      Authorization: `Bearer ${token}`,
    },
  };
}
