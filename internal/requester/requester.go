package requester

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/0xrinful/InkFetch/internal/models"
	"github.com/0xrinful/InkFetch/internal/requester/flare"
	"github.com/0xrinful/InkFetch/internal/sources"
)

var ErrSolverDisabled = errors.New("requester: flaresolver is disabled")

const defaultUserAgent = "Mozilla/5.0 (X11; Linux x86_64; rv:150.0) Gecko/20100101 Firefox/150.0"

type RequestMode int

const (
	ModeDefault RequestMode = iota
	ModeImage
)

type RequestOptions struct {
	Method  string
	Mode    RequestMode
	Headers map[string]string
	Body    []byte
}

type SessionStore interface {
	SourceSession(sourceID string) (*models.SourceSession, error)
	SaveSourceSession(session models.SourceSession) error
	DeleteSourceSession(sourceID string) error
}

type Requester struct {
	client         *http.Client
	solver         *flare.Solver
	store          SessionStore
	config         sources.Config
	session        *models.SourceSession
	sessionVersion uint64
	mu             sync.RWMutex
	group          singleflight.Group
	now            func() time.Time
}

func New(solver *flare.Solver, cfg sources.Config) *Requester {
	return NewWithStore(solver, nil, cfg)
}

func NewWithStore(solver *flare.Solver, store SessionStore, cfg sources.Config) *Requester {
	req := &Requester{
		client: &http.Client{Timeout: 180 * time.Second},
		solver: solver,
		store:  store,
		config: cfg,
		now:    time.Now,
	}

	req.loadStoredSession()
	return req
}

func (r *Requester) Get(ctx context.Context, url string) (*http.Response, error) {
	return r.Do(ctx, url, RequestOptions{Mode: ModeDefault})
}

func (r *Requester) GetImage(ctx context.Context, url string) (*http.Response, error) {
	return r.Do(ctx, url, RequestOptions{Mode: ModeImage})
}

func (r *Requester) Post(
	ctx context.Context,
	url string,
	contentType string,
	body []byte,
) (*http.Response, error) {
	return r.Do(ctx, url, RequestOptions{
		Method: http.MethodPost,
		Mode:   ModeDefault,
		Headers: map[string]string{
			"Content-Type": contentType,
		},
		Body: body,
	})
}

func (r *Requester) Do(
	ctx context.Context,
	url string,
	opts RequestOptions,
) (*http.Response, error) {
	if r.config.NeedsFlare && r.solver != nil {
		if err := r.ensureSession(ctx, url); err != nil {
			return nil, err
		}
	}

	switch opts.Mode {
	case ModeImage:
		return r.doImage(ctx, url, opts)
	default:
		return r.doDefault(ctx, url, opts)
	}
}

func (r *Requester) doDefault(
	ctx context.Context,
	url string,
	opts RequestOptions,
) (*http.Response, error) {
	resp, usedVersion, err := r.send(ctx, url, opts)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode == http.StatusForbidden && r.solver != nil {
		resp.Body.Close()
		if err := r.refreshSession(ctx, url, usedVersion); err != nil {
			return nil, err
		}
		retryResp, _, err := r.send(ctx, url, opts)
		return retryResp, err
	}

	return resp, nil
}

func (r *Requester) doImage(
	ctx context.Context,
	url string,
	opts RequestOptions,
) (*http.Response, error) {
	// Retry up to 3 times with backoff before assuming a challenge block.
	for attempt := range 3 {
		resp, usedVersion, err := r.send(ctx, url, opts)
		if err != nil {
			return nil, err
		}

		if resp.StatusCode != http.StatusForbidden {
			return resp, nil
		}

		if attempt == 2 {
			if r.solver == nil {
				return resp, nil
			}
			// 3rd 403 — refresh session cookies and retry once.
			resp.Body.Close()
			if err := r.refreshSession(ctx, url, usedVersion); err != nil {
				return nil, err
			}
			retryResp, _, err := r.send(ctx, url, opts)
			return retryResp, err
		}

		resp.Body.Close()

		// Temporary 403 — small backoff and retry.
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Duration(attempt+1) * 300 * time.Millisecond):
		}
	}

	return nil, fmt.Errorf("requester: image request failed after all retries: %s", url)
}

func (r *Requester) send(
	ctx context.Context,
	url string,
	opts RequestOptions,
) (*http.Response, uint64, error) {
	method := opts.Method
	if method == "" {
		method = http.MethodGet
	}

	var body io.Reader
	if len(opts.Body) > 0 {
		body = bytes.NewReader(opts.Body)
	}

	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, 0, fmt.Errorf("requester: build request: %w", err)
	}

	// Apply domain headers.
	for k, v := range r.config.Headers {
		req.Header.Set(k, v)
	}
	// Apply per-request headers.
	for k, v := range opts.Headers {
		req.Header.Set(k, v)
	}

	usedVersion := r.applySession(req)

	resp, err := r.client.Do(req)
	return resp, usedVersion, err
}

func (r *Requester) loadStoredSession() {
	if r.store == nil || r.config.SourceID == "" {
		return
	}

	stored, err := r.store.SourceSession(r.config.SourceID)
	if err != nil {
		slog.Warn("failed to load cached source session from db",
			"source", r.config.SourceID,
			"error", err,
		)
		return
	}
	if stored == nil {
		return
	}

	now := r.nowUTC()
	r.mu.Lock()
	defer r.mu.Unlock()

	if !isSessionValid(stored, now) {
		slog.Debug("cached source session expired",
			"source", r.config.SourceID,
			"expires_at", stored.ExpiresAt,
		)
		return
	}

	r.session = stored
	r.sessionVersion = 1
	slog.Debug("loaded cached source session from db",
		"source", r.config.SourceID,
		"cookies", len(stored.Cookies),
		"expires_at", stored.ExpiresAt,
	)
}

func (r *Requester) ensureSession(ctx context.Context, url string) error {
	now := r.nowUTC()

	r.mu.RLock()
	valid := isSessionValid(r.session, now)
	version := r.sessionVersion
	r.mu.RUnlock()

	if valid {
		return nil
	}

	targetURL := url
	if targetURL == "" {
		targetURL = r.config.CloudflareTestURL
	}
	return r.refreshSession(ctx, targetURL, version)
}

func (r *Requester) refreshSession(ctx context.Context, url string, failedVersion uint64) error {
	if r.solver == nil {
		return ErrSolverDisabled
	}

	// Fast path: another goroutine already refreshed the session after failedVersion.
	if r.hasFreshSessionAfter(failedVersion) {
		return nil
	}

	_, err, _ := r.group.Do("flare-session", func() (any, error) {
		if r.hasFreshSessionAfter(failedVersion) {
			return nil, nil
		}

		flareResult, err := r.solver.GetCookies(ctx, url)
		if err != nil {
			return nil, err
		}

		now := r.nowUTC()
		cookies := make([]models.SourceCookie, 0, len(flareResult.Cookies))
		for _, c := range flareResult.Cookies {
			cookies = append(cookies, models.SourceCookie{
				Name:      c.Name,
				Value:     c.Value,
				Domain:    c.Domain,
				Path:      c.Path,
				ExpiresAt: c.ExpiresAt,
			})
		}

		expiresAt := flareResult.ExpiresAt
		if expiresAt.IsZero() {
			expiresAt = now.Add(flare.DefaultSessionTTL)
		}

		session := models.SourceSession{
			SourceID:  r.config.SourceID,
			Cookies:   cookies,
			UserAgent: flareResult.UserAgent,
			ExpiresAt: expiresAt.UTC(),
			UpdatedAt: now,
		}

		r.mu.Lock()
		r.session = &session
		r.sessionVersion++
		r.mu.Unlock()

		if r.store != nil && r.config.SourceID != "" {
			if err := r.store.SaveSourceSession(session); err != nil {
				slog.Warn("failed to save source session to db",
					"source", r.config.SourceID,
					"error", err,
				)
			}
		}

		slog.Info("refreshed source session",
			"source", r.config.SourceID,
			"cookies", len(session.Cookies),
			"expires_at", session.ExpiresAt,
		)

		return nil, nil
	})

	return err
}

func (r *Requester) hasFreshSessionAfter(failedVersion uint64) bool {
	now := r.nowUTC()
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.sessionVersion > failedVersion && isSessionValid(r.session, now)
}

func (r *Requester) applySession(req *http.Request) uint64 {
	now := r.nowUTC()

	r.mu.RLock()
	defer r.mu.RUnlock()

	if !isSessionValid(r.session, now) {
		req.Header.Set("User-Agent", defaultUserAgent)
		return r.sessionVersion
	}

	reqHost := ""
	if req.URL != nil {
		reqHost = req.URL.Hostname()
	}

	for _, c := range r.session.Cookies {
		if !c.ExpiresAt.IsZero() && !now.Before(c.ExpiresAt) {
			continue
		}
		if !cookieMatchesHost(c.Domain, reqHost) {
			continue
		}
		req.AddCookie(&http.Cookie{
			Name:  c.Name,
			Value: c.Value,
		})
	}

	ua := r.session.UserAgent
	if ua == "" {
		ua = defaultUserAgent
	}
	req.Header.Set("User-Agent", ua)
	return r.sessionVersion
}

func (r *Requester) nowUTC() time.Time {
	if r.now != nil {
		return r.now().UTC()
	}
	return time.Now().UTC()
}

func isSessionValid(s *models.SourceSession, now time.Time) bool {
	if s == nil {
		return false
	}
	if len(s.Cookies) == 0 && s.UserAgent == "" {
		return false
	}
	if !s.ExpiresAt.IsZero() && !now.Before(s.ExpiresAt) {
		return false
	}
	return true
}

func cookieMatchesHost(cookieDomain, reqHost string) bool {
	cookieDomain = strings.ToLower(strings.TrimSpace(cookieDomain))
	if cookieDomain == "" {
		return true
	}
	reqHost = strings.ToLower(strings.TrimSpace(reqHost))
	if reqHost == "" {
		return true
	}
	domain := strings.TrimPrefix(cookieDomain, ".")
	return reqHost == domain || strings.HasSuffix(reqHost, "."+domain)
}
