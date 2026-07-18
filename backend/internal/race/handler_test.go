package race_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/akkien/aviron/internal/middleware"
	"github.com/akkien/aviron/internal/race"
	"github.com/golang-jwt/jwt/v5"
)

func signTestToken(t *testing.T, secret []byte, userID string) string {
	t.Helper()

	claims := jwt.MapClaims{
		"sub": userID,
		"exp": time.Now().Add(time.Hour).Unix(),
	}
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(secret)
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}
	return token
}

func newTestHandler() *race.RaceHandler {
	repo := newFakeRepository()
	svc := race.NewRaceService(repo, []byte("test-secret"))
	return race.NewRaceHandler(svc)
}

func TestRaceHandler_Create_Created(t *testing.T) {
	secret := []byte("test-secret")
	h := newTestHandler()
	token := signTestToken(t, secret, "user-1")

	body := `{"name":"Morning Sprint","distance_meters":1000}`
	req := httptest.NewRequest(http.MethodPost, "/races", bytes.NewBufferString(body))
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()

	middleware.Auth(secret)(http.HandlerFunc(h.Create)).ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusCreated, rec.Body.String())
	}

	var resp map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if resp["name"] != "Morning Sprint" {
		t.Errorf("name = %v, want %q", resp["name"], "Morning Sprint")
	}
	if resp["status"] != "pending" {
		t.Errorf("status = %v, want %q", resp["status"], "pending")
	}
	if resp["created_by"] != "user-1" {
		t.Errorf("created_by = %v, want %q", resp["created_by"], "user-1")
	}
}

func TestRaceHandler_Create_ValidationError(t *testing.T) {
	secret := []byte("test-secret")
	h := newTestHandler()
	token := signTestToken(t, secret, "user-1")

	body := `{"name":"","distance_meters":0}`
	req := httptest.NewRequest(http.MethodPost, "/races", bytes.NewBufferString(body))
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()

	middleware.Auth(secret)(http.HandlerFunc(h.Create)).ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
}

func TestRaceHandler_Create_InvalidBody(t *testing.T) {
	secret := []byte("test-secret")
	h := newTestHandler()
	token := signTestToken(t, secret, "user-1")

	req := httptest.NewRequest(http.MethodPost, "/races", bytes.NewBufferString("not json"))
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()

	middleware.Auth(secret)(http.HandlerFunc(h.Create)).ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
}

func TestRaceHandler_Create_MissingAuth(t *testing.T) {
	h := newTestHandler()

	body := `{"name":"Morning Sprint","distance_meters":1000}`
	req := httptest.NewRequest(http.MethodPost, "/races", bytes.NewBufferString(body))
	// No Authorization header, and not wrapped in middleware.Auth — simulates
	// the handler being invoked directly, to prove its own defensive check.
	rec := httptest.NewRecorder()

	h.Create(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusUnauthorized, rec.Body.String())
	}
}

// createTestRace registers a race directly through the handler and returns
// its id, so Join tests have a real race to join.
func createTestRace(t *testing.T, secret []byte, h *race.RaceHandler) string {
	t.Helper()

	token := signTestToken(t, secret, "creator")
	req := httptest.NewRequest(http.MethodPost, "/races", bytes.NewBufferString(
		`{"name":"Morning Sprint","distance_meters":1000}`))
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()

	middleware.Auth(secret)(http.HandlerFunc(h.Create)).ServeHTTP(rec, req)

	var resp map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode create-race body: %v", err)
	}
	return resp["id"].(string)
}

func TestRaceHandler_Join_OK(t *testing.T) {
	secret := []byte("test-secret")
	h := newTestHandler()
	raceID := createTestRace(t, secret, h)

	token := signTestToken(t, secret, "user-2")
	req := httptest.NewRequest(http.MethodPost, "/races/"+raceID+"/join", nil)
	req.SetPathValue("id", raceID)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()

	middleware.Auth(secret)(http.HandlerFunc(h.Join)).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if resp["race_id"] != raceID {
		t.Errorf("race_id = %v, want %q", resp["race_id"], raceID)
	}
	if resp["session_token"] == "" || resp["session_token"] == nil {
		t.Errorf("session_token = %v, want a non-empty JWT", resp["session_token"])
	}
}

func TestRaceHandler_Join_InvalidRaceID(t *testing.T) {
	secret := []byte("test-secret")
	h := newTestHandler()
	token := signTestToken(t, secret, "user-1")

	req := httptest.NewRequest(http.MethodPost, "/races/not-a-uuid/join", nil)
	req.SetPathValue("id", "not-a-uuid")
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()

	middleware.Auth(secret)(http.HandlerFunc(h.Join)).ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
}

func TestRaceHandler_Join_NotFound(t *testing.T) {
	secret := []byte("test-secret")
	h := newTestHandler()
	token := signTestToken(t, secret, "user-1")

	missingID := "00000000-0000-0000-0000-000000000000"
	req := httptest.NewRequest(http.MethodPost, "/races/"+missingID+"/join", nil)
	req.SetPathValue("id", missingID)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()

	middleware.Auth(secret)(http.HandlerFunc(h.Join)).ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusNotFound, rec.Body.String())
	}
}

func TestRaceHandler_Join_AlreadyJoined(t *testing.T) {
	secret := []byte("test-secret")
	h := newTestHandler()
	raceID := createTestRace(t, secret, h)
	token := signTestToken(t, secret, "user-2")

	join := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/races/"+raceID+"/join", nil)
		req.SetPathValue("id", raceID)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		middleware.Auth(secret)(http.HandlerFunc(h.Join)).ServeHTTP(rec, req)
		return rec
	}

	if rec := join(); rec.Code != http.StatusOK {
		t.Fatalf("first join status = %d, want %d, body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	rec := join()
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusConflict, rec.Body.String())
	}
}

func TestRaceHandler_Join_NotPending(t *testing.T) {
	secret := []byte("test-secret")
	repo := newFakeRepository()
	svc := race.NewRaceService(repo, secret)
	h := race.NewRaceHandler(svc)

	raceID := createTestRace(t, secret, h)
	repo.races[0].Status = "active"

	token := signTestToken(t, secret, "user-2")
	req := httptest.NewRequest(http.MethodPost, "/races/"+raceID+"/join", nil)
	req.SetPathValue("id", raceID)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()

	middleware.Auth(secret)(http.HandlerFunc(h.Join)).ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusConflict, rec.Body.String())
	}
}

func TestRaceHandler_Join_MissingAuth(t *testing.T) {
	h := newTestHandler()

	req := httptest.NewRequest(http.MethodPost, "/races/some-id/join", nil)
	req.SetPathValue("id", "some-id")
	// No Authorization header, and not wrapped in middleware.Auth — simulates
	// the handler being invoked directly, to prove its own defensive check.
	rec := httptest.NewRecorder()

	h.Join(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusUnauthorized, rec.Body.String())
	}
}
