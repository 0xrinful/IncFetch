package downloader

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/0xrinful/InkFetch/internal/models"
	"github.com/0xrinful/InkFetch/internal/requester"
)

const (
	defaultPageWorkers = 6
	maxPageRetries     = 3
)

type ProgressFunc func(completed, total int, bytesDelta int64)

type Downloader struct {
	req *requester.Requester
}

func New(req *requester.Requester) *Downloader {
	return &Downloader{req: req}
}

func (d *Downloader) DownloadChapter(
	ctx context.Context,
	chapter models.Chapter,
	destDir string,
) error {
	return d.DownloadChapterWithProgress(ctx, chapter, destDir, defaultPageWorkers, nil)
}

func (d *Downloader) DownloadChapterWithProgress(
	ctx context.Context,
	chapter models.Chapter,
	destDir string,
	workers int,
	onProgress ProgressFunc,
) error {
	if len(chapter.Pages) == 0 {
		return fmt.Errorf("downloader: chapter has no pages")
	}
	if workers < 1 {
		workers = defaultPageWorkers
	} else if workers > 16 {
		workers = 16
	}

	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return fmt.Errorf("downloader: ensure dir: %w", err)
	}

	var (
		mu        sync.Mutex
		wg        sync.WaitGroup
		errs      []error
		completed atomic.Int32
		total     = len(chapter.Pages)
		sem       = make(chan struct{}, workers)
	)

	if onProgress != nil {
		onProgress(0, total, 0)
	}

	for _, page := range chapter.Pages {
		if ctx.Err() != nil {
			break
		}

		wg.Go(func() {
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				return
			}

			n, err := d.downloadPageWithRetry(ctx, page, destDir)
			if err != nil {
				if ctx.Err() == nil {
					mu.Lock()
					errs = append(errs, err)
					mu.Unlock()
					slog.Error("downloader page failed", "page", page.Number, "url", page.URL, "err", err)
				}
				return
			}

			done := int(completed.Add(1))
			if onProgress != nil {
				mu.Lock()
				onProgress(done, total, n)
				mu.Unlock()
			}
		})
	}
	wg.Wait()

	if err := ctx.Err(); err != nil {
		return err
	}

	if len(errs) > 0 {
		return fmt.Errorf("chapter download failed on %d/%d pages (first: %v)", len(errs), total, errs[0])
	}
	return nil
}

func (d *Downloader) downloadPageWithRetry(
	ctx context.Context,
	page models.Page,
	destDir string,
) (int64, error) {
	var lastErr error
	for attempt := range maxPageRetries {
		if err := ctx.Err(); err != nil {
			return 0, err
		}

		n, retryable, err := d.downloadPage(ctx, page, destDir)
		if err == nil {
			return n, nil
		}
		lastErr = err
		if !retryable || attempt == maxPageRetries-1 {
			break
		}

		backoff := time.Duration(attempt+1) * 400 * time.Millisecond
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-time.After(backoff):
		}
	}
	return 0, lastErr
}

func (d *Downloader) downloadPage(
	ctx context.Context,
	page models.Page,
	destDir string,
) (int64, bool, error) {
	ext := extractExt(page.URL)
	name := fmt.Sprintf("%03d%s", page.Number, ext)
	fullpath := filepath.Join(destDir, name)

	resp, err := d.req.GetImage(ctx, page.URL)
	if err != nil {
		return 0, true, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		retryable := resp.StatusCode == http.StatusTooManyRequests ||
			resp.StatusCode >= http.StatusInternalServerError
		return 0, retryable, fmt.Errorf("page %d: HTTP %d", page.Number, resp.StatusCode)
	}

	n, err := saveToDiskAtomic(resp.Body, fullpath)
	if err != nil {
		return 0, true, err
	}
	if n == 0 {
		os.Remove(fullpath)
		return 0, true, fmt.Errorf("page %d: empty image body", page.Number)
	}
	return n, false, nil
}

func (d *Downloader) DownloadCover(ctx context.Context, url string, destPath string) error {
	if strings.TrimSpace(url) == "" {
		return nil
	}

	resp, err := d.req.GetImage(ctx, url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("cover download: HTTP %d", resp.StatusCode)
	}

	_, err = saveToDiskAtomic(resp.Body, destPath)
	return err
}

func saveToDiskAtomic(body io.Reader, destPath string) (int64, error) {
	tmpPath := destPath + ".part"
	file, err := os.Create(tmpPath)
	if err != nil {
		return 0, err
	}

	n, copyErr := io.Copy(file, body)
	closeErr := file.Close()
	if copyErr != nil {
		os.Remove(tmpPath)
		return 0, copyErr
	}
	if closeErr != nil {
		os.Remove(tmpPath)
		return 0, closeErr
	}

	if err := os.Rename(tmpPath, destPath); err != nil {
		os.Remove(tmpPath)
		return 0, err
	}
	return n, nil
}

func extractExt(rawURL string) string {
	if i := strings.IndexByte(rawURL, '?'); i != -1 {
		rawURL = rawURL[:i]
	}
	ext := strings.ToLower(filepath.Ext(rawURL))
	switch ext {
	case ".jpg", ".jpeg", ".png", ".webp", ".gif", ".avif":
		return ext
	default:
		return ".jpg"
	}
}
