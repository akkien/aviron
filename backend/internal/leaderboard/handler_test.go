package leaderboard_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/akkien/aviron/internal/leaderboard"
	"github.com/akkien/aviron/internal/middleware"
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

func newTestHandler(repo *fakeRepository) *leaderboard.LeaderboardHandler {
	svc := leaderboard.NewLeaderboardService(repo)
	return leaderboard.NewLeaderboardHandler(svc)
}

func TestHandler_Me_OK(t *testing.T) {
	secret := []byte("test-secret")
	repo := newFakeRepository()
	repo.stats["user-1"] = leaderboard.Stats{TotalRaces: 2, TotalWins: 1, TotalPaceWattSum: 100}
	h := newTestHandler(repo)
	token := signTestToken(t, secret, "user-1")

	req := httptest.NewRequest(http.MethodGet, "/leaderboard/me", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()

	middleware.Auth(secret)(http.HandlerFunc(h.Me)).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp leaderboard.LeaderboardMeResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	want := leaderboard.LeaderboardMeResponse{RacesJoined: 2, RacesWon: 1, AvgWPM: 50}
	if resp != want {
		t.Errorf("resp = %+v, want %+v", resp, want)
	}
}

func TestHandler_Me_NoRacesYet_AllZero(t *testing.T) {
	secret := []byte("test-secret")
	repo := newFakeRepository()
	h := newTestHandler(repo)
	token := signTestToken(t, secret, "brand-new-user")

	req := httptest.NewRequest(http.MethodGet, "/leaderboard/me", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()

	middleware.Auth(secret)(http.HandlerFunc(h.Me)).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp leaderboard.LeaderboardMeResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	want := leaderboard.LeaderboardMeResponse{RacesJoined: 0, RacesWon: 0, AvgWPM: 0}
	if resp != want {
		t.Errorf("resp = %+v, want %+v (a new account should get all-zero stats, not a 404)", resp, want)
	}
}

func TestHandler_Me_MissingAuth(t *testing.T) {
	repo := newFakeRepository()
	h := newTestHandler(repo)

	req := httptest.NewRequest(http.MethodGet, "/leaderboard/me", nil)
	// No Authorization header, and not wrapped in middleware.Auth — simulates
	// the handler being invoked directly, to prove its own defensive check.
	rec := httptest.NewRecorder()

	h.Me(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusUnauthorized, rec.Body.String())
	}
}

func TestHandler_Top_OK(t *testing.T) {
	secret := []byte("test-secret")
	repo := newFakeRepository()
	repo.top[leaderboard.WindowAllTime] = []leaderboard.Entry{
		{UserID: "user-1", DisplayName: "Alice", Races: 5, Wins: 3, AvgWPM: 60},
	}
	h := newTestHandler(repo)
	token := signTestToken(t, secret, "user-1")

	req := httptest.NewRequest(http.MethodGet, "/leaderboard?window=alltime", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()

	middleware.Auth(secret)(http.HandlerFunc(h.Top)).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp leaderboard.LeaderboardTopResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if resp.Window != "alltime" || resp.Page != 1 || len(resp.Entries) != 1 || resp.Entries[0].Rank != 1 {
		t.Errorf("resp = %+v, want window=alltime, page=1, one rank-1 entry", resp)
	}
}

func TestHandler_Top_PageQueryParam(t *testing.T) {
	secret := []byte("test-secret")
	repo := newFakeRepository()
	entries := make([]leaderboard.Entry, 7)
	for i := range entries {
		entries[i] = leaderboard.Entry{UserID: "user"}
	}
	repo.top[leaderboard.WindowAllTime] = entries
	h := newTestHandler(repo)
	token := signTestToken(t, secret, "user-1")

	req := httptest.NewRequest(http.MethodGet, "/leaderboard?window=alltime&page=2", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()

	middleware.Auth(secret)(http.HandlerFunc(h.Top)).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp leaderboard.LeaderboardTopResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	// 7 entries, 5 per page: page 2 has the remaining 2, first ranked 6.
	if resp.Page != 2 || resp.TotalPages != 2 || len(resp.Entries) != 2 || resp.Entries[0].Rank != 6 {
		t.Errorf("resp = %+v, want page=2, total_pages=2, 2 entries starting at rank 6", resp)
	}
}

func TestHandler_Top_InvalidWindow(t *testing.T) {
	secret := []byte("test-secret")
	repo := newFakeRepository()
	h := newTestHandler(repo)
	token := signTestToken(t, secret, "user-1")

	req := httptest.NewRequest(http.MethodGet, "/leaderboard?window=monthly", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()

	middleware.Auth(secret)(http.HandlerFunc(h.Top)).ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
}

func TestHandler_Top_MissingAuth(t *testing.T) {
	repo := newFakeRepository()
	h := newTestHandler(repo)

	req := httptest.NewRequest(http.MethodGet, "/leaderboard?window=alltime", nil)
	rec := httptest.NewRecorder()

	h.Top(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusUnauthorized, rec.Body.String())
	}
}
