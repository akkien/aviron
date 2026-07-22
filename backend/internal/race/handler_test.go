package race_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/akkien/aviron/internal/middleware"
	"github.com/akkien/aviron/internal/race"
	"github.com/akkien/aviron/internal/room"
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
	return race.NewRaceHandler(svc, room.NewRegistry(), context.Background())
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

	missingID := "999999999999"
	req := httptest.NewRequest(http.MethodPost, "/races/"+missingID+"/join", nil)
	req.SetPathValue("id", missingID)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()

	middleware.Auth(secret)(http.HandlerFunc(h.Join)).ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusNotFound, rec.Body.String())
	}
}

// TestRaceHandler_Join_AlreadyJoinedPending_ReturnsFreshTokenNotConflict
// covers idempotent-join.md: a repeat join for a still-pending race (e.g. a
// client recovering a lost session_token after a reload) now gets 200 +
// a fresh token instead of a 409.
func TestRaceHandler_Join_AlreadyJoinedPending_ReturnsFreshTokenNotConflict(t *testing.T) {
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
	if rec.Code != http.StatusOK {
		t.Fatalf("second join status = %d, want %d, body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if resp["session_token"] == "" || resp["session_token"] == nil {
		t.Error("second join response has no session_token")
	}
}

// TestRaceHandler_Join_AlreadyJoinedActive_RecoversToken is the actual gap
// this feature exists to close: recovering a session_token for a race
// already in progress, which previously 409'd race_not_pending
// unconditionally with no way around it.
func TestRaceHandler_Join_AlreadyJoinedActive_RecoversToken(t *testing.T) {
	secret := []byte("test-secret")
	h := newTestHandler()
	raceID := createTestRace(t, secret, h)

	joinReq := httptest.NewRequest(http.MethodPost, "/races/"+raceID+"/join", nil)
	joinReq.SetPathValue("id", raceID)
	joinReq.Header.Set("Authorization", "Bearer "+signTestToken(t, secret, "user-2"))
	joinRec := httptest.NewRecorder()
	middleware.Auth(secret)(http.HandlerFunc(h.Join)).ServeHTTP(joinRec, joinReq)
	if joinRec.Code != http.StatusOK {
		t.Fatalf("join status = %d, want %d, body = %s", joinRec.Code, http.StatusOK, joinRec.Body.String())
	}

	startReq := httptest.NewRequest(http.MethodPost, "/races/"+raceID+"/start", nil)
	startReq.SetPathValue("id", raceID)
	startReq.Header.Set("Authorization", "Bearer "+signTestToken(t, secret, "creator"))
	startRec := httptest.NewRecorder()
	middleware.Auth(secret)(http.HandlerFunc(h.Start)).ServeHTTP(startRec, startReq)
	if startRec.Code != http.StatusOK {
		t.Fatalf("start status = %d, want %d, body = %s", startRec.Code, http.StatusOK, startRec.Body.String())
	}

	rejoinReq := httptest.NewRequest(http.MethodPost, "/races/"+raceID+"/join", nil)
	rejoinReq.SetPathValue("id", raceID)
	rejoinReq.Header.Set("Authorization", "Bearer "+signTestToken(t, secret, "user-2"))
	rejoinRec := httptest.NewRecorder()
	middleware.Auth(secret)(http.HandlerFunc(h.Join)).ServeHTTP(rejoinRec, rejoinReq)

	if rejoinRec.Code != http.StatusOK {
		t.Fatalf("rejoin (active race) status = %d, want %d, body = %s", rejoinRec.Code, http.StatusOK, rejoinRec.Body.String())
	}
}

func TestRaceHandler_Join_RaceFull(t *testing.T) {
	secret := []byte("test-secret")
	h := newTestHandler()
	raceID := createTestRace(t, secret, h)

	join := func(userID string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/races/"+raceID+"/join", nil)
		req.SetPathValue("id", raceID)
		req.Header.Set("Authorization", "Bearer "+signTestToken(t, secret, userID))
		rec := httptest.NewRecorder()
		middleware.Auth(secret)(http.HandlerFunc(h.Join)).ServeHTTP(rec, req)
		return rec
	}

	// createTestRace's creator already occupies one slot (auto-joined by
	// CreateRace), so only MaxParticipants-1 more joins fit before full.
	for i := 0; i < race.MaxParticipants-1; i++ {
		userID := fmt.Sprintf("user-%d", i+2)
		if rec := join(userID); rec.Code != http.StatusOK {
			t.Fatalf("join(%s) status = %d, want %d, body = %s", userID, rec.Code, http.StatusOK, rec.Body.String())
		}
	}

	rec := join("one-too-many")
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusConflict, rec.Body.String())
	}
}

func TestRaceHandler_Join_NotPending(t *testing.T) {
	secret := []byte("test-secret")
	repo := newFakeRepository()
	svc := race.NewRaceService(repo, secret)
	h := race.NewRaceHandler(svc, room.NewRegistry(), context.Background())

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

func startRace(t *testing.T, secret []byte, h *race.RaceHandler, raceID, callerToken string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(http.MethodPost, "/races/"+raceID+"/start", nil)
	req.SetPathValue("id", raceID)
	req.Header.Set("Authorization", "Bearer "+callerToken)
	rec := httptest.NewRecorder()
	middleware.Auth(secret)(http.HandlerFunc(h.Start)).ServeHTTP(rec, req)
	return rec
}

func TestRaceHandler_Start_OK(t *testing.T) {
	secret := []byte("test-secret")
	h := newTestHandler()
	raceID := createTestRace(t, secret, h)
	creatorToken := signTestToken(t, secret, "creator")

	rec := startRace(t, secret, h, raceID, creatorToken)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if resp["status"] != "active" {
		t.Errorf("status = %v, want %q", resp["status"], "active")
	}
	if resp["prompt_text"] == "" || resp["prompt_text"] == nil {
		t.Errorf("prompt_text = %v, want a non-empty string", resp["prompt_text"])
	}
	if resp["started_at"] == "" || resp["started_at"] == nil {
		t.Errorf("started_at = %v, want a non-empty timestamp", resp["started_at"])
	}
}

func TestRaceHandler_Start_Forbidden(t *testing.T) {
	secret := []byte("test-secret")
	h := newTestHandler()
	raceID := createTestRace(t, secret, h)
	notCreatorToken := signTestToken(t, secret, "someone-else")

	rec := startRace(t, secret, h, raceID, notCreatorToken)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusForbidden, rec.Body.String())
	}
}

func TestRaceHandler_Start_NotFound(t *testing.T) {
	secret := []byte("test-secret")
	h := newTestHandler()
	token := signTestToken(t, secret, "creator")

	missingID := "999999999999"
	rec := startRace(t, secret, h, missingID, token)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusNotFound, rec.Body.String())
	}
}

func TestRaceHandler_Start_NotPending(t *testing.T) {
	secret := []byte("test-secret")
	h := newTestHandler()
	raceID := createTestRace(t, secret, h)
	creatorToken := signTestToken(t, secret, "creator")

	if rec := startRace(t, secret, h, raceID, creatorToken); rec.Code != http.StatusOK {
		t.Fatalf("first start status = %d, want %d, body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	rec := startRace(t, secret, h, raceID, creatorToken)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusConflict, rec.Body.String())
	}
}

func TestRaceHandler_Start_InvalidRaceID(t *testing.T) {
	secret := []byte("test-secret")
	h := newTestHandler()
	token := signTestToken(t, secret, "creator")

	rec := startRace(t, secret, h, "not-a-uuid", token)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
}

func TestRaceHandler_Start_MissingAuth(t *testing.T) {
	h := newTestHandler()

	req := httptest.NewRequest(http.MethodPost, "/races/some-id/start", nil)
	req.SetPathValue("id", "some-id")
	rec := httptest.NewRecorder()

	h.Start(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusUnauthorized, rec.Body.String())
	}
}

func TestRaceHandler_Text_OK(t *testing.T) {
	secret := []byte("test-secret")
	h := newTestHandler()
	raceID := createTestRace(t, secret, h)
	creatorToken := signTestToken(t, secret, "creator")

	if rec := startRace(t, secret, h, raceID, creatorToken); rec.Code != http.StatusOK {
		t.Fatalf("start status = %d, want %d, body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	req := httptest.NewRequest(http.MethodGet, "/races/"+raceID+"/text", nil)
	req.SetPathValue("id", raceID)
	req.Header.Set("Authorization", "Bearer "+creatorToken)
	rec := httptest.NewRecorder()

	middleware.Auth(secret)(http.HandlerFunc(h.Text)).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if resp["prompt_text"] == "" || resp["prompt_text"] == nil {
		t.Errorf("prompt_text = %v, want a non-empty string", resp["prompt_text"])
	}
}

func TestRaceHandler_Text_NotReady(t *testing.T) {
	secret := []byte("test-secret")
	h := newTestHandler()
	raceID := createTestRace(t, secret, h)
	token := signTestToken(t, secret, "creator")

	req := httptest.NewRequest(http.MethodGet, "/races/"+raceID+"/text", nil)
	req.SetPathValue("id", raceID)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()

	middleware.Auth(secret)(http.HandlerFunc(h.Text)).ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusConflict, rec.Body.String())
	}
}

func TestRaceHandler_Text_NotFound(t *testing.T) {
	secret := []byte("test-secret")
	h := newTestHandler()
	token := signTestToken(t, secret, "creator")

	missingID := "999999999999"
	req := httptest.NewRequest(http.MethodGet, "/races/"+missingID+"/text", nil)
	req.SetPathValue("id", missingID)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()

	middleware.Auth(secret)(http.HandlerFunc(h.Text)).ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusNotFound, rec.Body.String())
	}
}

func TestRaceHandler_Status_OK(t *testing.T) {
	secret := []byte("test-secret")
	h := newTestHandler()
	raceID := createTestRace(t, secret, h)

	req := httptest.NewRequest(http.MethodGet, "/races/"+raceID, nil)
	req.SetPathValue("id", raceID)
	req.Header.Set("Authorization", "Bearer "+signTestToken(t, secret, "user-1"))
	rec := httptest.NewRecorder()

	middleware.Auth(secret)(http.HandlerFunc(h.Status)).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if resp["name"] != "Morning Sprint" {
		t.Errorf("name = %v, want %q", resp["name"], "Morning Sprint")
	}
	if _, ok := resp["participants"]; !ok {
		t.Errorf("participants key missing from response: %v", resp)
	}
}

func TestRaceHandler_Status_PendingRaceIncludesPendingExpiresAt(t *testing.T) {
	secret := []byte("test-secret")
	repo := newFakeRepository()
	svc := race.NewRaceService(repo, secret)
	h := race.NewRaceHandler(svc, room.NewRegistry(), context.Background())
	raceID := createTestRace(t, secret, h)

	req := httptest.NewRequest(http.MethodGet, "/races/"+raceID, nil)
	req.SetPathValue("id", raceID)
	req.Header.Set("Authorization", "Bearer "+signTestToken(t, secret, "user-1"))
	rec := httptest.NewRecorder()

	middleware.Auth(secret)(http.HandlerFunc(h.Status)).ServeHTTP(rec, req)

	var resp map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	raw, ok := resp["pending_expires_at"].(string)
	if !ok {
		t.Fatalf("pending_expires_at missing or not a string: %v", resp)
	}
	got, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		t.Fatalf("pending_expires_at not RFC3339: %v", err)
	}
	want := repo.races[0].CreatedAt.Add(room.PendingTimeoutDuration)
	if diff := got.Sub(want); diff < -time.Second || diff > time.Second {
		t.Errorf("pending_expires_at = %v, want ~%v (created_at + PendingTimeoutDuration)", got, want)
	}
}

// TestRaceHandler_Status_FinishedRaceIncludesFinishResults covers
// race-detail-cold-visit.md: GET /races/{id} must expose finish results so
// a cold visit (reload, fresh link, a spectator) can render them without
// ever needing the live race_finished WebSocket message.
func TestRaceHandler_Status_FinishedRaceIncludesFinishResults(t *testing.T) {
	secret := []byte("test-secret")
	repo := newFakeRepository()
	svc := race.NewRaceService(repo, secret)
	h := race.NewRaceHandler(svc, room.NewRegistry(), context.Background())
	raceID := createTestRace(t, secret, h) // creator auto-joins as "creator"

	rank := 1
	finishTimeMs := int64(45000)
	results := []room.ParticipantResult{
		{UserID: "creator", FinishRank: &rank, FinishTimeMs: &finishTimeMs, AvgPaceWatt: 42.5},
	}
	if err := svc.FinishRace(context.Background(), raceID, 1000, results); err != nil {
		t.Fatalf("FinishRace() error = %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/races/"+raceID, nil)
	req.SetPathValue("id", raceID)
	req.Header.Set("Authorization", "Bearer "+signTestToken(t, secret, "user-1"))
	rec := httptest.NewRecorder()

	middleware.Auth(secret)(http.HandlerFunc(h.Status)).ServeHTTP(rec, req)

	var resp struct {
		Status       string `json:"status"`
		Participants []struct {
			UserID       string  `json:"user_id"`
			FinishRank   *int    `json:"finish_rank"`
			FinishTimeMs *int64  `json:"finish_time_ms"`
			AvgPaceWatt  float64 `json:"avg_pace_watt"`
		} `json:"participants"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if resp.Status != "finished" {
		t.Fatalf("status = %q, want %q", resp.Status, "finished")
	}
	if len(resp.Participants) != 1 {
		t.Fatalf("participants = %d, want 1", len(resp.Participants))
	}
	p := resp.Participants[0]
	if p.FinishRank == nil || *p.FinishRank != 1 {
		t.Errorf("finish_rank = %v, want 1", p.FinishRank)
	}
	if p.FinishTimeMs == nil || *p.FinishTimeMs != 45000 {
		t.Errorf("finish_time_ms = %v, want 45000", p.FinishTimeMs)
	}
	if p.AvgPaceWatt != 42.5 {
		t.Errorf("avg_pace_watt = %v, want 42.5", p.AvgPaceWatt)
	}
}

func TestRaceHandler_Status_ActiveRaceOmitsPendingExpiresAt(t *testing.T) {
	secret := []byte("test-secret")
	repo := newFakeRepository()
	svc := race.NewRaceService(repo, secret)
	h := race.NewRaceHandler(svc, room.NewRegistry(), context.Background())
	raceID := createTestRace(t, secret, h)
	repo.races[0].Status = "active"

	req := httptest.NewRequest(http.MethodGet, "/races/"+raceID, nil)
	req.SetPathValue("id", raceID)
	req.Header.Set("Authorization", "Bearer "+signTestToken(t, secret, "user-1"))
	rec := httptest.NewRecorder()

	middleware.Auth(secret)(http.HandlerFunc(h.Status)).ServeHTTP(rec, req)

	var resp map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if resp["pending_expires_at"] != nil {
		t.Errorf("pending_expires_at = %v, want nil for an active race", resp["pending_expires_at"])
	}
}

func TestRaceHandler_Status_NotFound(t *testing.T) {
	secret := []byte("test-secret")
	h := newTestHandler()
	token := signTestToken(t, secret, "user-1")

	missingID := "999999999999"
	req := httptest.NewRequest(http.MethodGet, "/races/"+missingID, nil)
	req.SetPathValue("id", missingID)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()

	middleware.Auth(secret)(http.HandlerFunc(h.Status)).ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusNotFound, rec.Body.String())
	}
}

func TestRaceHandler_ListOpen_OK(t *testing.T) {
	secret := []byte("test-secret")
	h := newTestHandler()
	raceID := createTestRace(t, secret, h) // created by "creator"

	req := httptest.NewRequest(http.MethodGet, "/races", nil)
	req.Header.Set("Authorization", "Bearer "+signTestToken(t, secret, "user-1"))
	rec := httptest.NewRecorder()

	middleware.Auth(secret)(http.HandlerFunc(h.ListOpen)).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp struct {
		Races []map[string]any `json:"races"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if len(resp.Races) != 1 {
		t.Fatalf("races = %+v, want exactly 1", resp.Races)
	}
	if resp.Races[0]["id"] != raceID {
		t.Errorf("id = %v, want %q", resp.Races[0]["id"], raceID)
	}
	if resp.Races[0]["host_display_name"] != "creator" {
		t.Errorf("host_display_name = %v, want %q", resp.Races[0]["host_display_name"], "creator")
	}
}

func TestRaceHandler_ListOpen_ExcludesCallersOwnRace(t *testing.T) {
	secret := []byte("test-secret")
	h := newTestHandler()
	createTestRace(t, secret, h) // created by "creator"

	req := httptest.NewRequest(http.MethodGet, "/races", nil)
	req.Header.Set("Authorization", "Bearer "+signTestToken(t, secret, "creator"))
	rec := httptest.NewRecorder()

	middleware.Auth(secret)(http.HandlerFunc(h.ListOpen)).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp struct {
		Races []map[string]any `json:"races"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if len(resp.Races) != 0 {
		t.Errorf("races = %+v, want none (caller is the creator)", resp.Races)
	}
}

func TestRaceHandler_ListOpen_MissingAuth(t *testing.T) {
	h := newTestHandler()

	req := httptest.NewRequest(http.MethodGet, "/races", nil)
	// No Authorization header, and not wrapped in middleware.Auth — simulates
	// the handler being invoked directly, to prove its own defensive check.
	rec := httptest.NewRecorder()

	h.ListOpen(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusUnauthorized, rec.Body.String())
	}
}
