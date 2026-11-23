package mangalek

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
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
		Headers:    map[string]string{"Referer": srv.URL + "/"},
	})
	return &Source{req: req, baseURL: srv.URL}
}

func TestLatestAndSearch(t *testing.T) {
	var seenActions []string
	var sawAltQuery bool

	src := newTestSource(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/wp-admin/admin-ajax.php" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("X-Requested-With") != "XMLHttpRequest" {
			t.Errorf("missing X-Requested-With header")
		}
		if err := r.ParseForm(); err != nil {
			t.Fatalf("parse form: %v", err)
		}
		seenActions = append(seenActions, r.FormValue("action"))

		// Verify filterNonMangaItems = false (Mangalek.kt)
		if r.FormValue("vars[meta_query][0][key]") != "" {
			t.Errorf("expected no filterNonMangaItems meta_query, got %q", r.FormValue("vars[meta_query][0][key]"))
		}

		if r.FormValue("vars[meta_query][alternative][key]") == "_wp_manga_alternative" {
			sawAltQuery = true
			fmt.Fprint(w, `
				<div class="manga__item">
					<div class="post-title"><h5><a href="/manga/alt-match/">Alt Title Match</a></h5></div>
					<img data-lazy-src="/uploads/alt.jpg" />
				</div>
				<div class="page-item-detail">
					<div class="post-title"><h3><a href="/manga/shared-slug/">Duplicate</a></h3></div>
					<img data-src="https://cdn.example/shared.jpg" />
				</div>
			`)
			return
		}

		fmt.Fprint(w, `
			<div class="page-item-detail">
				<div class="post-title"><h3><a href="/manga/shared-slug/">Solo Leveling</a></h3></div>
				<div class="item-thumb">
					<img src="/placeholder.gif" srcset="https://cdn.example/small.jpg 175w, https://cdn.example/large.jpg 350w" />
				</div>
			</div>
		`)
	})

	latest, err := src.Latest(context.Background(), 1, 25)
	if err != nil {
		t.Fatalf("Latest failed: %v", err)
	}
	if len(latest) != 1 || latest[0].Slug != "shared-slug" || latest[0].CoverURL != "https://cdn.example/large.jpg" {
		t.Fatalf("unexpected latest result: %+v", latest)
	}

	results, err := src.Search(context.Background(), "solo", 1, 25)
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if !sawAltQuery {
		t.Fatalf("expected concurrent alternative-name search request")
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 deduplicated results, got %d: %+v", len(results), results)
	}
	if results[0].Slug != "shared-slug" || results[1].Slug != "alt-match" {
		t.Fatalf("unexpected search slugs: %+v", results)
	}
	if !strings.HasSuffix(results[1].CoverURL, "/uploads/alt.jpg") {
		t.Fatalf("expected resolved relative cover URL, got %q", results[1].CoverURL)
	}
}

func TestMangaAdminAjaxChapters(t *testing.T) {
	var ajaxPostID string

	src := newTestSource(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/manga/one-piece/":
			fmt.Fprint(w, `
				<html><body>
					<div class="post-title"><h1><span class="manga-title-badges">HOT</span>One Piece</h1></div>
					<div class="summary_image"><img data-src="https://cdn.example/op.jpg" src="data:image/gif;base64,R0lGODlhAQABAAAAACw=" /></div>
					<div class="description-summary"><div class="summary__content"><p>Pirate adventure.</p><p>Second paragraph.</p></div></div>
					<div class="summary-content">مستمرة</div>
					<div class="genres-content"><a href="/manga-genre/action/">Action</a></div>
					<div class="tags-content"><a href="/tag/shonen/">Shonen</a><a href="/tag/action/">action</a></div>
					<div id="manga-chapters-holder" data-id="9876"></div>
				</body></html>
			`)
		case "/wp-admin/admin-ajax.php":
			_ = r.ParseForm()
			if r.FormValue("action") != "manga_get_chapters" {
				t.Errorf("unexpected ajax action %q", r.FormValue("action"))
			}
			ajaxPostID = r.FormValue("manga")
			fmt.Fprint(w, `
				<ul>
					<li class="wp-manga-chapter">
						<a href="/manga/one-piece/chapter-100/">الفصل 100</a>
						<span class="chapter-release-date"><i>يناير 05, 2025</i></span>
					</li>
					<li class="wp-manga-chapter">
						<a href="/manga/one-piece/chapter-99/">الفصل 99</a>
						<span class="chapter-release-date">2025-01-01</span>
					</li>
				</ul>
			`)
		default:
			http.NotFound(w, r)
		}
	})

	manga, err := src.Manga(context.Background(), "one-piece")
	if err != nil {
		t.Fatalf("Manga failed: %v", err)
	}
	if ajaxPostID != "9876" {
		t.Fatalf("expected AdminAjax manga post id 9876, got %q", ajaxPostID)
	}
	if manga.Title != "One Piece" {
		t.Fatalf("expected title 'One Piece' (badge stripped), got %q", manga.Title)
	}
	if manga.Status != "ongoing" {
		t.Fatalf("expected normalized status 'ongoing', got %q", manga.Status)
	}
	if manga.CoverURL != "https://cdn.example/op.jpg" {
		t.Fatalf("unexpected cover URL: %q", manga.CoverURL)
	}
	if len(manga.Genres) != 2 || manga.Genres[0] != "Action" || manga.Genres[1] != "Shonen" {
		t.Fatalf("unexpected genres: %+v", manga.Genres)
	}
	if len(manga.Chapters) != 2 {
		t.Fatalf("expected 2 chapters, got %d", len(manga.Chapters))
	}
	if manga.Chapters[0].Number != 100 || manga.Chapters[0].ReleasedAt.Format("2006-01-02") != "2025-01-05" {
		t.Fatalf("unexpected chapter 0: %+v", manga.Chapters[0])
	}
	if manga.Chapters[1].Number != 99 || manga.Chapters[1].ReleasedAt.Format("2006-01-02") != "2025-01-01" {
		t.Fatalf("unexpected chapter 1: %+v", manga.Chapters[1])
	}

	chs, err := src.Chapters(context.Background(), "one-piece")
	if err != nil || len(chs) != 2 {
		t.Fatalf("Chapters() failed: err=%v len=%d", err, len(chs))
	}
}

func TestPagesSinglePagerAndLazyAttrs(t *testing.T) {
	var requestedListStyle bool

	src := newTestSource(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("style") == "list" {
			requestedListStyle = true
			fmt.Fprint(w, `
				<div class="reading-content">
					<div class="page-break"><img data-src="  https://cdn.example/p1.webp  " /></div>
					<div class="page-break"><img data-lazy-src="/images/p2.jpg" /></div>
				</div>
			`)
			return
		}
		fmt.Fprint(w, `<div id="single-pager"></div><div class="page-break"><img src="https://cdn.example/p1.webp" /></div>`)
	})

	pages, err := src.Pages(context.Background(), "/manga/demo/ch-1/")
	if err != nil {
		t.Fatalf("Pages failed: %v", err)
	}
	if !requestedListStyle {
		t.Fatalf("expected re-fetch with style=list when #single-pager is present")
	}
	if len(pages) != 2 {
		t.Fatalf("expected 2 pages, got %d: %+v", len(pages), pages)
	}
	if pages[0].URL != "https://cdn.example/p1.webp" || !strings.HasSuffix(pages[1].URL, "/images/p2.jpg") {
		t.Fatalf("unexpected page URLs: %+v", pages)
	}
}

func TestPagesProtectorDecryption(t *testing.T) {
	password := "secret-nonce-123"
	salt := []byte{0x01, 0x23, 0x45, 0x67, 0x89, 0xab, 0xcd, 0xef}
	rawURLs, _ := json.Marshal([]string{"https://cdn.example/1.jpg", "https://cdn.example/2.jpg"})
	doubleEncoded, _ := json.Marshal(string(rawURLs))

	// Encrypt with OpenSSL EVP_BytesToKey (MD5) + AES-256-CBC + PKCS7
	keyAndIV := make([]byte, 0, 48)
	var prev []byte
	for len(keyAndIV) < 48 {
		h := md5.New()
		h.Write(prev)
		h.Write([]byte(password))
		h.Write(salt)
		prev = h.Sum(nil)
		keyAndIV = append(keyAndIV, prev...)
	}
	pad := aes.BlockSize - (len(doubleEncoded) % aes.BlockSize)
	padded := append(doubleEncoded, bytes.Repeat([]byte{byte(pad)}, pad)...)
	ct := make([]byte, len(padded))
	block, _ := aes.NewCipher(keyAndIV[:32])
	cipher.NewCBCEncrypter(block, keyAndIV[32:48]).CryptBlocks(ct, padded)

	payload, _ := json.Marshal(chapterProtectorData{
		Ciphertext: base64.StdEncoding.EncodeToString(ct),
		Salt:       hex.EncodeToString(salt),
	})
	script := fmt.Sprintf("var wpmangaprotectornonce='%s';var chapter_data='%s';", password, string(payload))
	encodedScript := base64.StdEncoding.EncodeToString([]byte(script))

	src := newTestSource(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `<script id="chapter-protector-data" src="data:text/javascript;base64,%s"></script>`, encodedScript)
	})

	pages, err := src.Pages(context.Background(), "/manga/demo/ch-protected/")
	if err != nil {
		t.Fatalf("Pages protector decrypt failed: %v", err)
	}
	if len(pages) != 2 || pages[0].URL != "https://cdn.example/1.jpg" || pages[1].URL != "https://cdn.example/2.jpg" {
		t.Fatalf("unexpected decrypted pages: %+v", pages)
	}
}
