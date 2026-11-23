package flare

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestSolverParsesCookieExpiriesAndPrefersCFClearance(t *testing.T) {
	now := time.Date(2026, 11, 23, 20, 0, 0, 0, time.UTC)
	cfExp := now.Add(365 * 24 * time.Hour)
	shortExp := now.Add(30 * time.Minute)
	pastExp := now.Add(-1 * time.Hour)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "ok",
			"solution": map[string]any{
				"userAgent": "TestBrowser/1.0",
				"cookies": []map[string]any{
					{
						"name":   "expired_cookie",
						"value":  "drop-me",
						"domain": ".example.com",
						"expiry": pastExp.Unix(),
					},
					{
						"name":    "analytics",
						"value":   "short",
						"domain":  ".example.com",
						"expires": float64(shortExp.Unix()) + 0.5,
					},
					{
						"name":   "cf_clearance",
						"value":  "token-1y",
						"domain": ".example.com",
						"path":   "/",
						"expiry": cfExp.Unix(),
					},
					{
						"name":  "session_only",
						"value": "no-exp",
					},
				},
			},
		})
	}))
	defer srv.Close()

	solver := NewSolverWithURL(srv.URL)
	solver.now = func() time.Time { return now }

	res, err := solver.GetCookies(context.Background(), "https://example.com/manga/test/")
	if err != nil {
		t.Fatalf("GetCookies failed: %v", err)
	}

	if res.UserAgent != "TestBrowser/1.0" {
		t.Fatalf("unexpected UserAgent: %q", res.UserAgent)
	}
	if len(res.Cookies) != 3 {
		t.Fatalf("expected 3 non-expired cookies, got %d: %+v", len(res.Cookies), res.Cookies)
	}
	if !res.ExpiresAt.Equal(cfExp) {
		t.Fatalf("expected session ExpiresAt=%v (cf_clearance), got %v", cfExp, res.ExpiresAt)
	}
	if res.Cookies[0].Name != "analytics" || !res.Cookies[0].ExpiresAt.Equal(shortExp.Add(500*time.Millisecond)) {
		t.Fatalf("expected float expires parsed accurately, got %+v", res.Cookies[0])
	}
	if res.Cookies[2].Name != "session_only" || !res.Cookies[2].ExpiresAt.IsZero() {
		t.Fatalf("expected zero ExpiresAt for session-only cookie, got %+v", res.Cookies[2])
	}
}

func TestSolverExpiryFallbacks(t *testing.T) {
	now := time.Date(2026, 11, 23, 20, 0, 0, 0, time.UTC)
	earliest := now.Add(30 * 24 * time.Hour)
	later := now.Add(180 * 24 * time.Hour)

	// Without cf_clearance -> earliest non-zero expiry wins.
	cookies := []Cookie{
		{Name: "long", Value: "1", ExpiresAt: later},
		{Name: "short", Value: "2", ExpiresAt: earliest},
		{Name: "session", Value: "3"},
	}
	if got := computeSessionExpiry(cookies, now); !got.Equal(earliest) {
		t.Fatalf("expected earliest expiry %v, got %v", earliest, got)
	}

	// Session-only cookies -> DefaultSessionTTL fallback.
	sessionOnly := []Cookie{{Name: "sid", Value: "abc"}}
	wantFallback := now.Add(DefaultSessionTTL)
	if got := computeSessionExpiry(sessionOnly, now); !got.Equal(wantFallback) {
		t.Fatalf("expected fallback expiry %v, got %v", wantFallback, got)
	}
}
