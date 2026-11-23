package flare

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strings"
	"time"
)

const (
	DefaultURL        = "http://localhost:8191/v1"
	DefaultTimeout    = 5 * time.Minute
	DefaultSessionTTL = 24 * time.Hour
)

var ErrSolverFault = errors.New("flare-solver")

type Cookie struct {
	Name      string
	Value     string
	Domain    string
	Path      string
	ExpiresAt time.Time
}

type Result struct {
	Cookies   []Cookie
	UserAgent string
	ExpiresAt time.Time
}

type solveRequest struct {
	CMD        string `json:"cmd"`
	URL        string `json:"url"`
	MaxTimeout int64  `json:"maxTimeout"`
}

type solveResponse struct {
	Status   string   `json:"status"`
	Message  string   `json:"message"`
	Solution solution `json:"solution"`
}

type solution struct {
	Cookies   []wireCookie `json:"cookies"`
	UserAgent string       `json:"userAgent"`
}

type wireCookie struct {
	Name    string   `json:"name"`
	Value   string   `json:"value"`
	Domain  string   `json:"domain"`
	Path    string   `json:"path"`
	Expiry  *float64 `json:"expiry"`
	Expires *float64 `json:"expires"`
}

type Solver struct {
	url     string
	timeout time.Duration
	client  *http.Client
	now     func() time.Time
}

func NewSolver() *Solver {
	return NewSolverWithURL(DefaultURL)
}

func NewSolverWithURL(url string) *Solver {
	if url == "" {
		url = DefaultURL
	}
	return &Solver{
		url:     url,
		timeout: DefaultTimeout,
		client:  &http.Client{Timeout: DefaultTimeout},
		now:     time.Now,
	}
}

func (s *Solver) GetCookies(ctx context.Context, targetURL string) (*Result, error) {
	var body bytes.Buffer
	err := json.NewEncoder(&body).Encode(solveRequest{
		CMD:        "request.get",
		URL:        targetURL,
		MaxTimeout: s.timeout.Milliseconds(),
	})
	if err != nil {
		return nil, fmt.Errorf("%w: request marshal: %v", ErrSolverFault, err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.url, &body)
	if err != nil {
		return nil, fmt.Errorf("%w: build request: %v", ErrSolverFault, err)
	}

	req.Header.Set("Content-Type", "application/json")

	resp, err := s.client.Do(req)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) ||
			errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, fmt.Errorf("%w: http timeout", ErrSolverFault)
		}
		return nil, fmt.Errorf("%w: network call: %v", ErrSolverFault, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: status code %d", ErrSolverFault, resp.StatusCode)
	}

	var solveResp solveResponse
	if err := json.NewDecoder(resp.Body).Decode(&solveResp); err != nil {
		return nil, fmt.Errorf("%w: response decode: %v", ErrSolverFault, err)
	}

	if solveResp.Status != "ok" {
		return nil, fmt.Errorf("%w: target site failure: %s", ErrSolverFault, solveResp.Message)
	}

	nowFn := s.now
	if nowFn == nil {
		nowFn = time.Now
	}
	now := nowFn().UTC()

	cookies := parseWireCookies(solveResp.Solution.Cookies, now)
	return &Result{
		Cookies:   cookies,
		UserAgent: solveResp.Solution.UserAgent,
		ExpiresAt: computeSessionExpiry(cookies, now),
	}, nil
}

func parseWireCookies(raw []wireCookie, now time.Time) []Cookie {
	out := make([]Cookie, 0, len(raw))
	for _, wc := range raw {
		if wc.Name == "" {
			continue
		}
		exp := parseCookieExpiry(wc.Expiry, wc.Expires)
		// Drop cookies already expired at solve time (e.g. deleted cookies).
		if !exp.IsZero() && !exp.After(now) {
			continue
		}
		out = append(out, Cookie{
			Name:      wc.Name,
			Value:     wc.Value,
			Domain:    wc.Domain,
			Path:      wc.Path,
			ExpiresAt: exp,
		})
	}
	return out
}

func parseCookieExpiry(expiry, expires *float64) time.Time {
	var ts float64
	switch {
	case expiry != nil && *expiry > 0:
		ts = *expiry
	case expires != nil && *expires > 0:
		ts = *expires
	default:
		return time.Time{}
	}

	// Guard against millisecond Unix timestamps.
	if ts > 1e12 {
		ts /= 1000
	}

	sec, frac := math.Modf(ts)
	return time.Unix(int64(sec), int64(frac*1e9)).UTC()
}

func computeSessionExpiry(cookies []Cookie, now time.Time) time.Time {
	// Prefer cf_clearance's expiry when present so short-lived unrelated cookies
	// do not prematurely invalidate a long-lived clearance cookie.
	for _, c := range cookies {
		if strings.EqualFold(c.Name, "cf_clearance") && c.ExpiresAt.After(now) {
			return c.ExpiresAt.UTC()
		}
	}

	var earliest time.Time
	for _, c := range cookies {
		if c.ExpiresAt.IsZero() || !c.ExpiresAt.After(now) {
			continue
		}
		if earliest.IsZero() || c.ExpiresAt.Before(earliest) {
			earliest = c.ExpiresAt.UTC()
		}
	}
	if !earliest.IsZero() {
		return earliest
	}

	return now.Add(DefaultSessionTTL).UTC()
}
