package server_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/0xrinful/InkFetch/internal/models"
	"github.com/0xrinful/InkFetch/internal/queue"
	"github.com/0xrinful/InkFetch/internal/registry"
	"github.com/0xrinful/InkFetch/internal/server"
	"github.com/0xrinful/InkFetch/internal/service"
	"github.com/0xrinful/InkFetch/internal/storage/db"
	"github.com/0xrinful/InkFetch/internal/storage/files"
)

func setupTestApp(t *testing.T) (*server.Server, *db.DB, *files.Store, []*http.Cookie) {
	t.Helper()

	// Ensure relative ui/templates paths resolve from repo root.
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Clean(filepath.Join(wd, "..", ".."))
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(wd) })

	tmp := t.TempDir()
	database, err := db.New(filepath.Join(tmp, "test.db"))
	if err != nil {
		t.Fatalf("db.New: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })

	fileStore := files.New(filepath.Join(tmp, "data"))
	reg := registry.NewRegistry(nil, database)
	q := queue.NewQueue()
	svc := service.New(reg, database, fileStore, q)

	server.SetSessionSecret("test-secret-key")
	srv := server.New(svc)

	// Sign up a user and capture session cookie.
	form := url.Values{"email": {"reader@inkfetch.local"}, "password": {"password123"}}
	req := httptest.NewRequest(http.MethodPost, "/signup", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusFound {
		t.Fatalf("signup expected 302, got %d: %s", rec.Code, rec.Body.String())
	}

	return srv, database, fileStore, rec.Result().Cookies()
}

func doReq(t *testing.T, srv http.Handler, method, target string, cookies []*http.Cookie, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	var body *strings.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	} else {
		body = strings.NewReader("")
	}
	req := httptest.NewRequest(method, target, body)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

func TestInkFetchEndToEndScreensAndWorkflows(t *testing.T) {
	srv, database, fileStore, cookies := setupTestApp(t)

	// Seed a manga and two chapters (one downloaded with a page image, one missing).
	now := time.Now().UTC()
	m := models.MangaRecord{
		Manga: models.Manga{
			Slug:        "sakamoto-days",
			SourceID:    "teamx",
			Title:       "Sakamoto Days",
			Description: "Retired assassin runs a convenience store.",
			Status:      "ongoing",
			Genres:      []string{"Action", "Comedy"},
		},
		AddedAt:   now,
		UpdatedAt: now,
	}
	if err := database.SaveManga(m); err != nil {
		t.Fatalf("SaveManga: %v", err)
	}
	if err := database.AddFavorite(1, m.Slug, m.SourceID); err != nil {
		t.Fatalf("AddFavorite: %v", err)
	}

	ch1 := models.Chapter{SourceID: "teamx", MangaSlug: "sakamoto-days", Number: 1, Title: "The Legendary Hitman", URL: "https://example.com/1", ReleasedAt: now}
	ch2 := models.Chapter{SourceID: "teamx", MangaSlug: "sakamoto-days", Number: 2, Title: "Chapter 2", URL: "https://example.com/2", ReleasedAt: now}
	_ = database.SaveChapter(models.ChapterRecord{Chapter: ch1})
	_ = database.SaveChapter(models.ChapterRecord{Chapter: ch2})

	ch1Dir := fileStore.ChapterDir(ch1)
	if err := fileStore.EnsureDir(ch1Dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ch1Dir, "001.jpg"), []byte("imgdata"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := database.MarkDownloaded(ch1, ch1Dir); err != nil {
		t.Fatal(err)
	}

	// Verify all primary pages render cleanly with InkFetch branding and zero InkFetch leftovers.
	pages := []struct {
		path     string
		contains string
	}{
		{"/login", "InkFetch"},
		{"/signup", "Create account"},
		{"/", "Sakamoto Days"},
		{"/?q=sakamoto&status=ongoing&sort=title", "Sakamoto Days"},
		{"/sources/teamx", "Team X"},
		{"/manga/teamx/sakamoto-days", "Select Range"},
		{"/manga/teamx/sakamoto-days/ch/1", "Sakamoto Days"},
		{"/manga/teamx/sakamoto-days/ch/2", "not downloaded yet"},
		{"/downloads", "Pause Queue"},
		{"/settings", "Download &amp; Processing Pipeline"},
	}

	for _, tc := range pages {
		rec := doReq(t, srv, http.MethodGet, tc.path, cookies, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d, want 200. Body: %s", tc.path, rec.Code, rec.Body.String())
		}
		body := rec.Body.String()
		if !strings.Contains(body, tc.contains) {
			t.Fatalf("GET %s missing expected content %q", tc.path, tc.contains)
		}
		if strings.Contains(strings.ToLower(body), "zenq") {
			t.Fatalf("GET %s still contains legacy 'zenq' branding", tc.path)
		}
	}

	// Verify /dashboard redirects cleanly to /downloads.
	redir := doReq(t, srv, http.MethodGet, "/dashboard", cookies, nil)
	if redir.Code != http.StatusFound || redir.Header().Get("Location") != "/downloads" {
		t.Fatalf("/dashboard expected 302 -> /downloads, got %d -> %q", redir.Code, redir.Header().Get("Location"))
	}

	// Verify /sources redirects cleanly to first source.
	srcRedir := doReq(t, srv, http.MethodGet, "/sources", cookies, nil)
	if srcRedir.Code != http.StatusFound || !strings.HasPrefix(srcRedir.Header().Get("Location"), "/sources/") {
		t.Fatalf("/sources expected 302 -> /sources/<id>, got %d -> %q", srcRedir.Code, srcRedir.Header().Get("Location"))
	}

	// Verify reader pages JSON endpoint works for downloaded chapter.
	pagesRec := doReq(t, srv, http.MethodGet, "/manga/teamx/sakamoto-days/ch/1/pages", cookies, nil)
	if pagesRec.Code != http.StatusOK || !strings.Contains(pagesRec.Body.String(), "001.jpg") {
		t.Fatalf("reader pages failed: %d %s", pagesRec.Code, pagesRec.Body.String())
	}

	// Verify bulk chapter selection & toggle read.
	bulkRec := doReq(t, srv, http.MethodPost, "/manga/teamx/sakamoto-days/chapters/bulk", cookies, url.Values{
		"action":   {"download"},
		"chapters": {"2"},
	})
	if bulkRec.Code != http.StatusOK {
		t.Fatalf("bulk download failed: %d %s", bulkRec.Code, bulkRec.Body.String())
	}

	toggleRec := doReq(t, srv, http.MethodPost, "/manga/teamx/sakamoto-days/ch/2/read", cookies, nil)
	if toggleRec.Code != http.StatusOK || !strings.Contains(toggleRec.Body.String(), "is-read") {
		t.Fatalf("toggle read failed: %d %s", toggleRec.Code, toggleRec.Body.String())
	}

	// Verify queue pause/resume/jobs API & badge.
	pauseRec := doReq(t, srv, http.MethodPost, "/api/jobs/pause", cookies, nil)
	if pauseRec.Code != http.StatusOK || !strings.Contains(pauseRec.Body.String(), "Resume Queue") {
		t.Fatalf("pause queue failed: %d %s", pauseRec.Code, pauseRec.Body.String())
	}
	badgeRec := doReq(t, srv, http.MethodGet, "/api/downloads/badge", cookies, nil)
	if badgeRec.Code != http.StatusOK || !strings.Contains(badgeRec.Body.String(), "1") {
		t.Fatalf("downloads badge expected active count 1, got %d %s", badgeRec.Code, badgeRec.Body.String())
	}

	// Verify settings persistence.
	saveRec := doReq(t, srv, http.MethodPost, "/settings", cookies, url.Values{
		"download_concurrency": {"5"},
		"page_concurrency":     {"8"},
		"optimize_quality":     {"82"},
		"auto_optimize":        {"on"},
	})
	if saveRec.Code != http.StatusOK {
		t.Fatalf("POST /settings failed: %d %s", saveRec.Code, saveRec.Body.String())
	}
	loaded, err := database.LoadSettings()
	if err != nil {
		t.Fatalf("LoadSettings: %v", err)
	}
	if loaded.DownloadConcurrency != 5 || loaded.PageConcurrency != 8 || loaded.OptimizeQuality != 82 || !loaded.AutoOptimize || loaded.AutoPack {
		t.Fatalf("persisted settings mismatch: %+v", loaded)
	}

	// Verify context-aware RefreshManga doesn't panic on cancelled ctx.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest(http.MethodPost, "/manga/teamx/sakamoto-days/sync", nil).WithContext(ctx)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	syncRec := httptest.NewRecorder()
	srv.ServeHTTP(syncRec, req)
	if syncRec.Code == http.StatusNotFound {
		t.Fatalf("POST /sync returned 404; alias route not registered")
	}
}
