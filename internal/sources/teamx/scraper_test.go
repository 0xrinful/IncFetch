package teamx

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/0xrinful/InkFetch/internal/requester"
	"github.com/0xrinful/InkFetch/internal/sources"
)

func newTestSource(t *testing.T, handler http.HandlerFunc) *Source {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	req := requester.New(nil, sources.Config{
		BaseURL:    srv.URL,
		NeedsFlare: false,
	})
	return &Source{req: req, baseURL: srv.URL}
}

func TestLatestAndSearchKeyword(t *testing.T) {
	src := newTestSource(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			fmt.Fprint(w, `
				<div class="last-chapter">
					<div class="box">
						<div class="imgu"><img src="https://cdn.olympustaff.com/images/manga/thumbnail_hero.jpg" /></div>
						<div class="info"><a href="/series/hero-return"><h3>Hero Return</h3></a></div>
					</div>
				</div>
			`)
		case "/search":
			if r.URL.Query().Get("keyword") != "hero" {
				t.Errorf("expected keyword=hero, got %q", r.URL.Query().Get("keyword"))
			}
			fmt.Fprint(w, `
				<div class="tx-grid">
					<a class="tx-card" href="/series/hero-return">
						<img src="/images/manga/thumbnail_hero.jpg" />
						<h3>Hero Return</h3>
					</a>
				</div>
			`)
		default:
			http.NotFound(w, r)
		}
	})

	latest, err := src.Latest(context.Background(), 1, 25)
	if err != nil {
		t.Fatalf("Latest failed: %v", err)
	}
	if len(latest) != 1 || latest[0].CoverURL != "https://cdn.olympustaff.com/images/manga/hero.jpg" {
		t.Fatalf("unexpected latest: %+v", latest)
	}

	searched, err := src.Search(context.Background(), "hero", 1, 25)
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(searched) != 1 || searched[0].Slug != "hero-return" || searched[0].Title != "Hero Return" {
		t.Fatalf("unexpected search results: %+v", searched)
	}
	if !strings.HasSuffix(searched[0].CoverURL, "/images/manga/hero.jpg") {
		t.Fatalf("expected thumbnail_ removed from resolved URL, got %q", searched[0].CoverURL)
	}
}

func TestMangaConcurrentPaginationAndLockedSkip(t *testing.T) {
	src := newTestSource(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/series/hero-return" {
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("page") == "2" {
			fmt.Fprint(w, `
				<div class="chapter-card" data-number="1" data-date="1700000000">
					<a href="/series/hero-return/1"><div class="chapter-info"><div class="chapter-title">الفصل 1</div></div></a>
				</div>
			`)
			return
		}
		fmt.Fprint(w, `
			<div class="author-info-title"><h1>Hero Return</h1></div>
			<div class="review-content"><p>Great story.</p></div>
			<div class="text-right"><img src="/images/manga/hero.jpg" /></div>
			<div class="full-list-info"><small>الحالة</small><small>مستمرة</small></div>
			<div class="review-author-info"><a href="/genre/action">أكشن</a></div>
			<div class="chapter-card" data-number="3" data-date="1700000200">
				<span class="locked">Locked</span>
				<a href="/series/hero-return/3"><div class="chapter-info"><div class="chapter-title">VIP</div></div></a>
			</div>
			<div class="chapter-card" data-number="2" data-date="1700000100">
				<a href="/series/hero-return/2"><div class="chapter-info"><div class="chapter-title">البداية</div></div></a>
			</div>
			<ul class="pagination">
				<li><a class="page-link" href="?page=1">1</a></li>
				<li><a class="page-link" href="?page=2">2</a></li>
			</ul>
		`)
	})

	manga, err := src.Manga(context.Background(), "hero-return")
	if err != nil {
		t.Fatalf("Manga failed: %v", err)
	}
	if manga.Status != "ongoing" {
		t.Fatalf("expected status 'ongoing', got %q", manga.Status)
	}
	if len(manga.Chapters) != 2 {
		t.Fatalf("expected 2 unlocked chapters across 2 pages, got %d: %+v", len(manga.Chapters), manga.Chapters)
	}
	if manga.Chapters[0].Number != 2 || manga.Chapters[0].Title != "الفصل 2 - البداية" {
		t.Fatalf("unexpected chapter 0: %+v", manga.Chapters[0])
	}
	if manga.Chapters[1].Number != 1 || manga.Chapters[1].Title != "الفصل 1" {
		t.Fatalf("unexpected chapter 1: %+v", manga.Chapters[1])
	}
}

func TestPagesCanvasAndImg(t *testing.T) {
	src := newTestSource(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `
			<div class="image_list">
				<img src="" />
				<canvas data-src="/uploads/1.webp"></canvas>
				<img src="https://cdn.olympustaff.com/uploads/2.webp" />
			</div>
		`)
	})

	pages, err := src.Pages(context.Background(), "/series/hero-return/1")
	if err != nil {
		t.Fatalf("Pages failed: %v", err)
	}
	if len(pages) != 2 {
		t.Fatalf("expected 2 valid pages, got %d: %+v", len(pages), pages)
	}
	if pages[0].Number != 1 || !strings.HasSuffix(pages[0].URL, "/uploads/1.webp") {
		t.Fatalf("unexpected page 0: %+v", pages[0])
	}
	if pages[1].Number != 2 || pages[1].URL != "https://cdn.olympustaff.com/uploads/2.webp" {
		t.Fatalf("unexpected page 1: %+v", pages[1])
	}
}
