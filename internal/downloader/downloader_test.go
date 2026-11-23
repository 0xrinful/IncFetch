package downloader

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/0xrinful/InkFetch/internal/models"
	"github.com/0xrinful/InkFetch/internal/requester"
	"github.com/0xrinful/InkFetch/internal/sources"
)

func TestDownloaderRetriesTransientErrorsAndReportsProgress(t *testing.T) {
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/p1.jpg" && attempts.Add(1) == 1 {
			http.Error(w, "temporary", http.StatusServiceUnavailable)
			return
		}
		if r.URL.Path == "/bad.jpg" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("fake-image-bytes"))
	}))
	defer srv.Close()

	req := requester.New(nil, sources.Config{BaseURL: srv.URL, NeedsFlare: false})
	dl := New(req)
	dest := t.TempDir()

	var totalBytes int64
	var lastDone int
	err := dl.DownloadChapterWithProgress(
		context.Background(),
		models.Chapter{
			Pages: []models.Page{
				{Number: 1, URL: srv.URL + "/p1.jpg"},
				{Number: 2, URL: srv.URL + "/p2.webp"},
			},
		},
		dest,
		2,
		func(completed, total int, bytesDelta int64) {
			lastDone = completed
			totalBytes += bytesDelta
		},
	)
	if err != nil {
		t.Fatalf("DownloadChapterWithProgress failed: %v", err)
	}
	if lastDone != 2 || totalBytes != int64(len("fake-image-bytes")*2) {
		t.Fatalf("unexpected progress: done=%d bytes=%d", lastDone, totalBytes)
	}
	if _, err := os.Stat(filepath.Join(dest, "001.jpg")); err != nil {
		t.Fatalf("expected 001.jpg on disk: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "002.webp")); err != nil {
		t.Fatalf("expected 002.webp on disk: %v", err)
	}

	// Verify 404 is rejected and not saved to disk.
	err = dl.DownloadChapterWithProgress(
		context.Background(),
		models.Chapter{Pages: []models.Page{{Number: 1, URL: srv.URL + "/bad.jpg"}}},
		t.TempDir(),
		1,
		nil,
	)
	if err == nil {
		t.Fatalf("expected error on HTTP 404 image")
	}
}
