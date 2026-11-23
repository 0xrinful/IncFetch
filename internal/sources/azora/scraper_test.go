package azora

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
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
	return &Source{req: req, baseURL: srv.URL, apiURL: srv.URL}
}

func TestConfigUsesAzoraFly(t *testing.T) {
	cfg := Config()
	if cfg.BaseURL != "https://azorafly.com" {
		t.Fatalf("expected BaseURL https://azorafly.com, got %q", cfg.BaseURL)
	}
	if apiURL != "https://api.azorafly.com" {
		t.Fatalf("expected apiURL https://api.azorafly.com, got %q", apiURL)
	}
}

func TestLatestAndSearchUseQueryEndpointAndSkipNovelPages(t *testing.T) {
	var seenOrderBy string
	var seenPages []string

	src := newTestSource(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/query" {
			t.Errorf("expected /api/query, got %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		seenOrderBy = r.URL.Query().Get("orderBy")
		page := r.URL.Query().Get("page")
		seenPages = append(seenPages, page)

		if page == "1" {
			// Page 1 has only novels; Iken should advance to page 2.
			fmt.Fprint(w, `{"posts":[{"id":1,"slug":"novel-only","postTitle":"Novel","isNovel":true,"seriesType":"MANGA"}],"totalCount":36}`)
			return
		}
		fmt.Fprint(w, `{"posts":[{"id":2,"slug":"real-manga","postTitle":"Real Manga","isNovel":false,"seriesType":"MANHWA","featuredImage":"https://cdn.example/c.jpg"}],"totalCount":36}`)
	})

	mangas, err := src.Latest(context.Background(), 1, 18)
	if err != nil {
		t.Fatalf("Latest failed: %v", err)
	}
	if seenOrderBy != "lastChapterAddedAt" {
		t.Fatalf("expected orderBy=lastChapterAddedAt, got %q", seenOrderBy)
	}
	if len(seenPages) != 2 || seenPages[0] != "1" || seenPages[1] != "2" {
		t.Fatalf("expected novel-page skip from 1 -> 2, got %v", seenPages)
	}
	if len(mangas) != 1 || mangas[0].Slug != "real-manga" {
		t.Fatalf("unexpected mangas: %+v", mangas)
	}
}

func TestMangaFetchesFullChaptersAndParsesIkenSchema(t *testing.T) {
	var hitChaptersEndpoint bool

	src := newTestSource(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/post":
			fmt.Fprint(w, `{
				"totalChapterCount": 3,
				"post": {
					"id": 42,
					"slug": "omni-reader",
					"postTitle": "Omniscient Reader",
					"postContent": "<p>Hello &amp; welcome</p>\n<p>Line 2</p>",
					"alternativeTitles": "ORV",
					"featuredImage": "https://cdn.example/orv.jpg",
					"seriesStatus": "ONGOING",
					"seriesType": "MANHWA",
					"genres": [{"id":1,"name":"Action"}],
					"chapters": [
						{"id":100,"slug":"ch-1","number":1,"title":"Start","createdAt":"2025-01-01T00:00:00.000Z"}
					]
				}
			}`)
		case "/api/chapters":
			if r.URL.Query().Get("postId") != "42" {
				t.Errorf("expected postId=42, got %q", r.URL.Query().Get("postId"))
			}
			hitChaptersEndpoint = true
			// Notice: NO isAccessible field (must NOT treat as locked!), string number "1.5", and paid/locked chapters filtered out.
			fmt.Fprint(w, `{
				"post": {
					"chapters": [
						{"id":102,"slug":"ch-2","number":2,"title":"","createdAt":"2025-01-03T12:00:00Z","price":50,"chapterPurchased":false},
						{"id":101,"slug":"ch-1-5","number":"1.5","title":"Bonus","createdAt":"2025-01-02T12:00:00Z","isLocked":false,"price":0},
						{"id":100,"slug":"ch-1","number":1,"title":"Start","createdAt":"2025-01-01T00:00:00.000Z"}
					]
				}
			}`)
		default:
			http.NotFound(w, r)
		}
	})

	manga, err := src.Manga(context.Background(), "omni-reader")
	if err != nil {
		t.Fatalf("Manga failed: %v", err)
	}
	if !hitChaptersEndpoint {
		t.Fatalf("expected fallback to /api/chapters when totalChapterCount > len(post.chapters)")
	}
	if manga.Status != "ongoing" {
		t.Fatalf("expected status 'ongoing', got %q", manga.Status)
	}
	if len(manga.Genres) != 2 || manga.Genres[0] != "Manhwa" || manga.Genres[1] != "Action" {
		t.Fatalf("unexpected genres: %+v", manga.Genres)
	}
	if len(manga.Chapters) != 2 {
		t.Fatalf("expected 2 unlocked chapters, got %d: %+v", len(manga.Chapters), manga.Chapters)
	}
	if manga.Chapters[0].Number != 1.5 || manga.Chapters[0].URL != "101" {
		t.Fatalf("unexpected chapter 0: %+v", manga.Chapters[0])
	}
	if manga.Chapters[1].Number != 1 || manga.Chapters[1].URL != "100" {
		t.Fatalf("unexpected chapter 1: %+v", manga.Chapters[1])
	}
}

func TestPagesSortingAndSpaceEncoding(t *testing.T) {
	src := newTestSource(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chapter" || r.URL.Query().Get("chapterId") != "101" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, `{
			"chapter": {
				"id": 101,
				"images": [
					{"order": null, "url": "https://cdn.example/last image.jpg"},
					{"order": 2, "url": "https://cdn.example/page 2.jpg"},
					{"order": 1, "url": "https://cdn.example/page 1.jpg"}
				]
			}
		}`)
	})

	pages, err := src.Pages(context.Background(), "101")
	if err != nil {
		t.Fatalf("Pages failed: %v", err)
	}
	if len(pages) != 3 {
		t.Fatalf("expected 3 pages, got %d", len(pages))
	}
	want := []string{
		"https://cdn.example/page%201.jpg",
		"https://cdn.example/page%202.jpg",
		"https://cdn.example/last%20image.jpg",
	}
	for i, w := range want {
		if pages[i].URL != w || pages[i].Number != i+1 {
			t.Fatalf("page[%d] = %+v, want url=%q num=%d", i, pages[i], w, i+1)
		}
	}
}
