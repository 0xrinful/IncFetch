package requester

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/0xrinful/InkFetch/internal/requester/flare"
	"github.com/0xrinful/InkFetch/internal/sources"
	"github.com/0xrinful/InkFetch/internal/storage/db"
)

func TestRequesterWithoutSolverHandles403WithoutPanic(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "blocked", http.StatusForbidden)
	}))
	defer srv.Close()

	req := New(nil, sources.Config{
		SourceID:          "test",
		BaseURL:           srv.URL,
		NeedsFlare:        true,
		CloudflareTestURL: srv.URL,
	})

	resp, err := req.Get(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("expected 403 response without error when solver disabled, got err: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected status 403, got %d", resp.StatusCode)
	}

	imgResp, err := req.GetImage(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("expected 403 image response without error when solver disabled, got err: %v", err)
	}
	defer imgResp.Body.Close()
	if imgResp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected image status 403, got %d", imgResp.StatusCode)
	}
}

func TestRequesterWithSolverRetriesOn403(t *testing.T) {
	solverSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "ok",
			"solution": map[string]any{
				"cookies":   []map[string]any{{"name": "cf_clearance", "value": "solved", "expiry": time.Now().Add(30 * 24 * time.Hour).Unix()}},
				"userAgent": "SolvedUA/1.0",
			},
		})
	}))
	defer solverSrv.Close()

	targetSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie("cf_clearance")
		if err != nil || c.Value != "solved" || r.Header.Get("User-Agent") != "SolvedUA/1.0" {
			http.Error(w, "challenge", http.StatusForbidden)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer targetSrv.Close()

	req := New(flare.NewSolverWithURL(solverSrv.URL), sources.Config{
		SourceID:   "mangalek",
		BaseURL:    targetSrv.URL,
		NeedsFlare: false,
	})

	resp, err := req.Get(context.Background(), targetSrv.URL)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 after solver retry, got %d", resp.StatusCode)
	}
}

func TestRequesterPersistsCookiesInDBAcrossRestartsAndRefreshesOnExpiryOr403(t *testing.T) {
	database, err := db.New(filepath.Join(t.TempDir(), "requester.db"))
	if err != nil {
		t.Fatalf("db.New: %v", err)
	}
	defer database.Close()

	var (
		clockMu sync.Mutex
		now     = time.Now().UTC().Truncate(time.Second)
	)
	getNow := func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		return now
	}
	setNow := func(ts time.Time) {
		clockMu.Lock()
		defer clockMu.Unlock()
		now = ts
	}

	var solverCalls atomic.Int32
	var validToken atomic.Value
	validToken.Store("token-v1")

	solverSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callNum := solverCalls.Add(1)
		tok := "token-v1"
		exp := getNow().Add(30 * 24 * time.Hour)
		if callNum == 2 {
			tok = "token-v2"
			exp = getNow().Add(90 * 24 * time.Hour)
		} else if callNum >= 3 {
			tok = "token-v3"
			exp = getNow().Add(365 * 24 * time.Hour)
		}
		validToken.Store(tok)

		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "ok",
			"solution": map[string]any{
				"userAgent": "SolverUA/" + tok,
				"cookies": []map[string]any{
					{
						"name":   "cf_clearance",
						"value":  tok,
						"expiry": exp.Unix(),
					},
				},
			},
		})
	}))
	defer solverSrv.Close()

	targetSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		expected := validToken.Load().(string)
		c, err := r.Cookie("cf_clearance")
		if err != nil || c.Value != expected || r.Header.Get("User-Agent") != "SolverUA/"+expected {
			http.Error(w, "challenge", http.StatusForbidden)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer targetSrv.Close()

	cfg := sources.Config{
		SourceID:   "mangalek",
		BaseURL:    targetSrv.URL,
		NeedsFlare: false,
	}

	// 1. First run: no cached session -> 403 triggers solver once and saves cookies + expiry to DB.
	r1 := NewWithStore(flare.NewSolverWithURL(solverSrv.URL), database, cfg)
	r1.now = getNow

	resp, err := r1.Get(context.Background(), targetSrv.URL+"/manga/one-piece/")
	if err != nil {
		t.Fatalf("first Get failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if solverCalls.Load() != 1 {
		t.Fatalf("expected 1 solver call, got %d", solverCalls.Load())
	}

	stored, err := database.SourceSession("mangalek")
	if err != nil || stored == nil {
		t.Fatalf("expected stored session in db, got (%+v, %v)", stored, err)
	}
	if len(stored.Cookies) != 1 || stored.Cookies[0].Value != "token-v1" {
		t.Fatalf("unexpected stored cookies: %+v", stored.Cookies)
	}
	if !stored.ExpiresAt.Equal(getNow().Add(30 * 24 * time.Hour)) {
		t.Fatalf("unexpected stored expiry: %v", stored.ExpiresAt)
	}

	// 2. Simulate app restart even with solver DISABLED (nil): DB cookies are loaded and reused!
	rOffline := NewWithStore(nil, database, cfg)
	rOffline.now = getNow

	resp2, err := rOffline.Get(context.Background(), targetSrv.URL+"/manga/one-piece/")
	if err != nil {
		t.Fatalf("restarted offline Get failed: %v", err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 using DB-cached cookies without solver, got %d", resp2.StatusCode)
	}
	if solverCalls.Load() != 1 {
		t.Fatalf("expected no additional solver call on restart, got %d", solverCalls.Load())
	}

	// 3. Simulate early 403 before expiry (server revokes token-v1 early).
	validToken.Store("revoked-until-refresh")
	r2 := NewWithStore(flare.NewSolverWithURL(solverSrv.URL), database, cfg)
	r2.now = getNow

	resp3, err := r2.Get(context.Background(), targetSrv.URL+"/manga/one-piece/")
	if err != nil {
		t.Fatalf("403 refresh Get failed: %v", err)
	}
	resp3.Body.Close()
	if resp3.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 after 403 refresh, got %d", resp3.StatusCode)
	}
	if solverCalls.Load() != 2 {
		t.Fatalf("expected 2nd solver call on 403, got %d", solverCalls.Load())
	}

	stored2, err := database.SourceSession("mangalek")
	if err != nil || stored2 == nil || stored2.Cookies[0].Value != "token-v2" {
		t.Fatalf("expected DB updated to token-v2, got %+v (err=%v)", stored2, err)
	}

	// 4. Advance clock past 90-day expiry -> expired cookies are not sent and solver refreshes to token-v3!
	setNow(getNow().Add(91 * 24 * time.Hour))
	resp4, err := r2.Get(context.Background(), targetSrv.URL+"/manga/one-piece/")
	if err != nil {
		t.Fatalf("expired refresh Get failed: %v", err)
	}
	resp4.Body.Close()
	if resp4.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 after expiry refresh, got %d", resp4.StatusCode)
	}
	if solverCalls.Load() != 3 {
		t.Fatalf("expected 3rd solver call after expiry, got %d", solverCalls.Load())
	}

	stored3, err := database.SourceSession("mangalek")
	if err != nil || stored3 == nil || stored3.Cookies[0].Value != "token-v3" {
		t.Fatalf("expected DB updated to token-v3, got %+v (err=%v)", stored3, err)
	}
}

func TestRequesterNeedsFlareHasNoBackgroundLoopAndRefreshesOnlyOnExpiry(t *testing.T) {
	database, err := db.New(filepath.Join(t.TempDir(), "needsflare.db"))
	if err != nil {
		t.Fatalf("db.New: %v", err)
	}
	defer database.Close()

	var solverCalls atomic.Int32
	now := time.Now().UTC().Truncate(time.Second)

	solverSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		solverCalls.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "ok",
			"solution": map[string]any{
				"userAgent": "NF-UA",
				"cookies": []map[string]any{
					{"name": "cf_clearance", "value": "nf-ok", "expiry": now.Add(60 * 24 * time.Hour).Unix()},
				},
			},
		})
	}))
	defer solverSrv.Close()

	targetSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie("cf_clearance")
		if err != nil || c.Value != "nf-ok" {
			http.Error(w, "challenge", http.StatusForbidden)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer targetSrv.Close()

	cfg := sources.Config{
		SourceID:          "protected",
		BaseURL:           targetSrv.URL,
		CloudflareTestURL: targetSrv.URL,
		NeedsFlare:        true,
	}

	req := NewWithStore(flare.NewSolverWithURL(solverSrv.URL), database, cfg)
	req.now = func() time.Time { return now }

	// No background loop should fire on startup before any request.
	time.Sleep(50 * time.Millisecond)
	if solverCalls.Load() != 0 {
		t.Fatalf("expected 0 background solver calls on startup, got %d", solverCalls.Load())
	}

	// First request triggers solve once.
	resp, err := req.Get(context.Background(), targetSrv.URL)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || solverCalls.Load() != 1 {
		t.Fatalf("expected 200 and 1 solver call, got status=%d calls=%d", resp.StatusCode, solverCalls.Load())
	}

	// New requester loads from DB -> still 1 total solver call.
	req2 := NewWithStore(flare.NewSolverWithURL(solverSrv.URL), database, cfg)
	req2.now = func() time.Time { return now }
	resp2, err := req2.Get(context.Background(), targetSrv.URL)
	if err != nil {
		t.Fatalf("second Get failed: %v", err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK || solverCalls.Load() != 1 {
		t.Fatalf("expected DB reuse without re-solving, got status=%d calls=%d", resp2.StatusCode, solverCalls.Load())
	}

	// Advance past expiry -> proactive ensureSession refreshes once more.
	now = now.Add(61 * 24 * time.Hour)
	resp3, err := req2.Get(context.Background(), targetSrv.URL)
	if err != nil {
		t.Fatalf("third Get failed: %v", err)
	}
	resp3.Body.Close()
	if resp3.StatusCode != http.StatusOK || solverCalls.Load() != 2 {
		t.Fatalf("expected proactive refresh after expiry, got status=%d calls=%d", resp3.StatusCode, solverCalls.Load())
	}
}

func TestRequesterConcurrent403DeduplicatesSolverCalls(t *testing.T) {
	var solverCalls atomic.Int32
	solverSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		solverCalls.Add(1)
		time.Sleep(40 * time.Millisecond)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "ok",
			"solution": map[string]any{
				"userAgent": "DedupUA",
				"cookies": []map[string]any{
					{"name": "cf_clearance", "value": "fresh", "expiry": time.Now().Add(time.Hour).Unix()},
				},
			},
		})
	}))
	defer solverSrv.Close()

	targetSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie("cf_clearance")
		if err != nil || c.Value != "fresh" {
			http.Error(w, "challenge", http.StatusForbidden)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer targetSrv.Close()

	req := New(flare.NewSolverWithURL(solverSrv.URL), sources.Config{
		SourceID:   "mangalek",
		BaseURL:    targetSrv.URL,
		NeedsFlare: false,
	})

	const workers = 8
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, err := req.Get(context.Background(), targetSrv.URL)
			if err != nil {
				errs <- err
				return
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				errs <- fmt.Errorf("unexpected status %d", resp.StatusCode)
			}
		}()
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent request failed: %v", err)
		}
	}
	if got := solverCalls.Load(); got != 1 {
		t.Fatalf("expected 1 deduplicated solver call across %d concurrent 403s, got %d", workers, got)
	}
}

func TestCookieMatchesHost(t *testing.T) {
	cases := []struct {
		domain string
		host   string
		want   bool
	}{
		{"", "anything.example", true},
		{".mangalik.net", "mangalik.net", true},
		{".mangalik.net", "cdn.mangalik.net", true},
		{"mangalik.net", "mangalik.net", true},
		{".mangalik.net", "fakemangalik.net", false},
		{".mangalik.net", "othercdn.com", false},
	}
	for _, tc := range cases {
		if got := cookieMatchesHost(tc.domain, tc.host); got != tc.want {
			t.Errorf("cookieMatchesHost(%q, %q) = %v, want %v", tc.domain, tc.host, got, tc.want)
		}
	}
}
