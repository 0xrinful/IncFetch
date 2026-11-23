package teamx

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	"golang.org/x/sync/errgroup"

	"github.com/0xrinful/InkFetch/internal/helpers/parse"
	"github.com/0xrinful/InkFetch/internal/models"
)

const (
	thumbnailSuffix           = "thumbnail_"
	chapterFetchConcurrency   = 5
)

// ─── Latest ──────────────────────────────────────────────────────────────────
// The home page lists recently updated series inside "div.last-chapter div.box".

func (s *Source) Latest(ctx context.Context, page, size int) ([]models.Manga, error) {
	targetURL := s.baseURL
	if page > 1 {
		targetURL = fmt.Sprintf("%s?page=%d", s.baseURL, page)
	}

	resp, err := s.req.Get(ctx, targetURL)
	if err != nil {
		return nil, fmt.Errorf("teamx: latest: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("teamx: latest: status %d", resp.StatusCode)
	}

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("teamx: parse latest: %w", err)
	}

	seen := make(map[string]struct{})
	var mangas []models.Manga

	doc.Find("div.last-chapter div.box").Each(func(_ int, sel *goquery.Selection) {
		if size > 0 && len(mangas) >= size {
			return
		}
		link := sel.Find("div.info a").First()
		title := strings.TrimSpace(link.Find("h3").Text())
		if title == "" {
			return
		}

		href := resolveURL(s.baseURL, link.AttrOr("href", ""))
		slug := extractSlug(href)
		if slug == "" {
			return
		}
		if _, dup := seen[slug]; dup {
			return
		}
		seen[slug] = struct{}{}

		coverURL := resolveURL(s.baseURL, sel.Find("div.imgu img").First().AttrOr("src", ""))
		coverURL = strings.ReplaceAll(coverURL, thumbnailSuffix, "")

		mangas = append(mangas, models.Manga{
			Slug:     slug,
			SourceID: sourceID,
			Title:    title,
			CoverURL: coverURL,
		})
	})

	return mangas, nil
}

// ─── Search ──────────────────────────────────────────────────────────────────
// TeamX serves search results at /search?keyword=... (div.tx-grid a.tx-card).
// For browse-without-query we hit /series?page=N (div.listupd div.bsx).

func (s *Source) Search(ctx context.Context, query string, page, size int) ([]models.Manga, error) {
	query = strings.TrimSpace(query)
	if query != "" {
		return s.searchKeyword(ctx, query, size)
	}
	return s.browseSeries(ctx, page, size)
}

func (s *Source) searchKeyword(ctx context.Context, query string, size int) ([]models.Manga, error) {
	params := url.Values{"keyword": {query}}
	targetURL := fmt.Sprintf("%s/search?%s", s.baseURL, params.Encode())

	resp, err := s.req.Get(ctx, targetURL)
	if err != nil {
		return nil, fmt.Errorf("teamx: search: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("teamx: search: status %d", resp.StatusCode)
	}

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("teamx: parse search: %w", err)
	}

	var mangas []models.Manga

	// Matches TeamX.kt: document.select("div.tx-grid a.tx-card")
	doc.Find("div.tx-grid a.tx-card").Each(func(_ int, sel *goquery.Selection) {
		if size > 0 && len(mangas) >= size {
			return
		}
		title := strings.TrimSpace(sel.Find("h3").Text())
		if title == "" {
			return
		}
		href := resolveURL(s.baseURL, sel.AttrOr("href", ""))
		slug := extractSlug(href)
		if slug == "" {
			return
		}

		img := sel.Find("img").First()
		coverRaw := img.AttrOr("src", "")
		if coverRaw == "" {
			coverRaw = img.AttrOr("data-src", "")
		}
		coverURL := strings.ReplaceAll(resolveURL(s.baseURL, coverRaw), thumbnailSuffix, "")

		mangas = append(mangas, models.Manga{
			Slug:     slug,
			SourceID: sourceID,
			Title:    title,
			CoverURL: coverURL,
		})
	})

	return mangas, nil
}

func (s *Source) browseSeries(ctx context.Context, page, size int) ([]models.Manga, error) {
	if page < 1 {
		page = 1
	}
	targetURL := fmt.Sprintf("%s/series?page=%d", s.baseURL, page)

	resp, err := s.req.Get(ctx, targetURL)
	if err != nil {
		return nil, fmt.Errorf("teamx: browse: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("teamx: browse: status %d", resp.StatusCode)
	}

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("teamx: parse browse: %w", err)
	}

	var mangas []models.Manga

	// /series grid: div.listupd div.bsx
	doc.Find("div.listupd div.bsx").Each(func(_ int, sel *goquery.Selection) {
		if size > 0 && len(mangas) >= size {
			return
		}
		a := sel.Find("a").First()
		title := strings.TrimSpace(a.AttrOr("title", ""))
		if title == "" {
			title = strings.TrimSpace(a.Text())
		}
		href := resolveURL(s.baseURL, a.AttrOr("href", ""))
		slug := extractSlug(href)
		if slug == "" {
			return
		}

		img := sel.Find("img").First()
		coverRaw := strings.TrimSpace(img.AttrOr("data-src", ""))
		if coverRaw == "" {
			coverRaw = strings.TrimSpace(img.AttrOr("src", ""))
		}
		coverURL := resolveURL(s.baseURL, coverRaw)

		mangas = append(mangas, models.Manga{
			Slug:     slug,
			SourceID: sourceID,
			Title:    title,
			CoverURL: coverURL,
		})
	})

	return mangas, nil
}

// ─── Manga Detail ────────────────────────────────────────────────────────────

func (s *Source) Manga(ctx context.Context, slug string) (*models.Manga, error) {
	targetURL := fmt.Sprintf("%s/series/%s", s.baseURL, strings.Trim(slug, "/"))

	resp, err := s.req.Get(ctx, targetURL)
	if err != nil {
		return nil, fmt.Errorf("teamx: manga: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("teamx: manga: status %d", resp.StatusCode)
	}

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("teamx: parse manga: %w", err)
	}

	resolvedURL := targetURL
	if resp.Request != nil && resp.Request.URL != nil {
		u := *resp.Request.URL
		u.RawQuery = ""
		resolvedURL = u.String()
	}

	manga := &models.Manga{
		Slug:     slug,
		SourceID: sourceID,
	}

	manga.Title = strings.TrimSpace(doc.Find("div.author-info-title h1").First().Text())

	// Description: prefer div.review-content text, fall back to <p> children.
	desc := strings.TrimSpace(doc.Find("div.review-content").Text())
	if desc == "" {
		var parts []string
		doc.Find("div.review-content p").Each(func(_ int, sel *goquery.Selection) {
			if t := strings.TrimSpace(sel.Text()); t != "" {
				parts = append(parts, t)
			}
		})
		desc = strings.Join(parts, "\n")
	}
	manga.Description = desc

	coverImg := doc.Find("div.text-right img").First()
	coverRaw := strings.TrimSpace(coverImg.AttrOr("src", ""))
	if coverRaw == "" {
		coverRaw = strings.TrimSpace(coverImg.AttrOr("data-src", ""))
	}
	manga.CoverURL = resolveURL(resolvedURL, coverRaw)

	// Status: the <small> sibling after ".full-list-info > small:first-child" containing "الحالة"
	doc.Find(".full-list-info > small:first-child").Each(func(_ int, sel *goquery.Selection) {
		if strings.Contains(sel.Text(), "الحالة") {
			manga.Status = normalizeStatus(sel.Next().Text())
		}
	})

	// Genres
	doc.Find("div.review-author-info a").Each(func(_ int, sel *goquery.Selection) {
		if g := strings.TrimSpace(sel.Text()); g != "" {
			manga.Genres = append(manga.Genres, g)
		}
	})

	manga.Chapters, err = s.fetchAllChapters(ctx, slug, resolvedURL, doc)
	if err != nil {
		return nil, err
	}

	return manga, nil
}

// fetchAllChapters collects chapters from page 1 and fetches remaining pages
// concurrently via pagination numbers (TeamX.kt), with rel=next fallback.
func (s *Source) fetchAllChapters(
	ctx context.Context,
	slug, mangaURL string,
	firstDoc *goquery.Document,
) ([]models.Chapter, error) {
	firstPage := s.parseChapterElements(firstDoc, slug, mangaURL)

	lastPage := 1
	firstDoc.Find("ul.pagination a.page-link").Each(func(_ int, sel *goquery.Selection) {
		if n, err := strconv.Atoi(strings.TrimSpace(sel.Text())); err == nil && n > lastPage {
			lastPage = n
		}
	})

	if lastPage > 1 {
		remaining := make([][]models.Chapter, lastPage-1)
		g, gctx := errgroup.WithContext(ctx)
		g.SetLimit(chapterFetchConcurrency)

		for page := 2; page <= lastPage; page++ {
			idx := page - 2
			pageURL := fmt.Sprintf("%s?page=%d", mangaURL, page)

			g.Go(func() error {
				resp, err := s.req.Get(gctx, pageURL)
				if err != nil {
					return fmt.Errorf("teamx: chapters page %d: %w", page, err)
				}
				defer resp.Body.Close()

				if resp.StatusCode < 200 || resp.StatusCode >= 300 {
					return fmt.Errorf("teamx: chapters page %d: status %d", page, resp.StatusCode)
				}

				doc, err := goquery.NewDocumentFromReader(resp.Body)
				if err != nil {
					return fmt.Errorf("teamx: parse chapters page %d: %w", page, err)
				}

				remaining[idx] = s.parseChapterElements(doc, slug, pageURL)
				return nil
			})
		}

		if err := g.Wait(); err != nil {
			return firstPage, err
		}

		total := len(firstPage)
		for _, pageChs := range remaining {
			total += len(pageChs)
		}
		chapters := make([]models.Chapter, 0, total)
		chapters = append(chapters, firstPage...)
		for _, pageChs := range remaining {
			chapters = append(chapters, pageChs...)
		}
		return chapters, nil
	}

	// Fallback: follow <a rel="next"> if numbered pagination links weren't present.
	chapters := firstPage
	doc := firstDoc
	for {
		next := resolveURL(mangaURL, doc.Find("a[rel=next]").First().AttrOr("href", ""))
		if next == "" {
			break
		}

		resp, err := s.req.Get(ctx, next)
		if err != nil {
			return chapters, fmt.Errorf("teamx: chapters next page: %w", err)
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			resp.Body.Close()
			return chapters, fmt.Errorf("teamx: chapters next page: status %d", resp.StatusCode)
		}

		doc, err = goquery.NewDocumentFromReader(resp.Body)
		resp.Body.Close()
		if err != nil {
			return chapters, fmt.Errorf("teamx: parse chapters next page: %w", err)
		}

		chapters = append(chapters, s.parseChapterElements(doc, slug, next)...)
	}

	return chapters, nil
}

func (s *Source) parseChapterElements(
	doc *goquery.Document,
	slug, pageURL string,
) []models.Chapter {
	var chapters []models.Chapter

	doc.Find("div.chapter-card").Each(func(_ int, sel *goquery.Selection) {
		// Skip locked chapters (TeamX.kt: element.selectFirst("span.locked") != null).
		if sel.Find("span.locked").Length() > 0 {
			return
		}

		rawHref := strings.TrimSpace(sel.Find("a").First().AttrOr("href", ""))
		if rawHref == "" || rawHref == "#" {
			return
		}
		chURL := resolveURL(pageURL, rawHref)
		if chURL == "" {
			return
		}

		numberStr := strings.TrimSpace(sel.AttrOr("data-number", ""))
		number, err := strconv.ParseFloat(numberStr, 64)

		titleText := strings.TrimSpace(sel.Find("div.chapter-info div.chapter-title").Text())
		if err != nil {
			number, _ = parse.ParseChapterNumber(titleText)
			if numberStr == "" {
				numberStr = strconv.FormatFloat(number, 'f', -1, 64)
			}
		}

		title := fmt.Sprintf("الفصل %s", numberStr)
		if titleText != "" &&
			titleText != numberStr &&
			titleText != title &&
			titleText != fmt.Sprintf("الفصل رقم %s", numberStr) {
			title = fmt.Sprintf("%s - %s", title, titleText)
		}

		// data-date is a Unix timestamp in seconds.
		var releasedAt time.Time
		if ts, err := strconv.ParseInt(strings.TrimSpace(sel.AttrOr("data-date", "")), 10, 64); err == nil && ts > 0 {
			releasedAt = time.Unix(ts, 0).UTC()
		}

		chapters = append(chapters, models.Chapter{
			SourceID:   sourceID,
			MangaSlug:  slug,
			Title:      title,
			Number:     number,
			URL:        chURL,
			ReleasedAt: releasedAt,
		})
	})

	return chapters
}

// ─── Chapters ────────────────────────────────────────────────────────────────

func (s *Source) Chapters(ctx context.Context, slug string) ([]models.Chapter, error) {
	manga, err := s.Manga(ctx, slug)
	if err != nil {
		return nil, err
	}
	return manga.Chapters, nil
}

// ─── Pages ───────────────────────────────────────────────────────────────────

func (s *Source) Pages(ctx context.Context, chapterURL string) ([]models.Page, error) {
	targetURL := resolveURL(s.baseURL, chapterURL)
	if targetURL == "" {
		return nil, fmt.Errorf("teamx: empty chapter url")
	}

	resp, err := s.req.Get(ctx, targetURL)
	if err != nil {
		return nil, fmt.Errorf("teamx: pages: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("teamx: pages: status %d", resp.StatusCode)
	}

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("teamx: parse pages: %w", err)
	}

	resolvedURL := targetURL
	if resp.Request != nil && resp.Request.URL != nil {
		resolvedURL = resp.Request.URL.String()
	}

	var pages []models.Page

	// Images are inside div.image_list, either <canvas data-src> or <img src>.
	doc.Find("div.image_list canvas[data-src], div.image_list img[src]").
		Each(func(_ int, sel *goquery.Selection) {
			var rawURL string
			if src, ok := sel.Attr("src"); ok && strings.TrimSpace(src) != "" {
				rawURL = src
			} else {
				rawURL = sel.AttrOr("data-src", "")
			}

			imgURL := resolveURL(resolvedURL, rawURL)
			if imgURL == "" {
				return
			}
			pages = append(pages, models.Page{
				Number: len(pages) + 1,
				URL:    imgURL,
			})
		})

	return pages, nil
}

// ─── Helpers ─────────────────────────────────────────────────────────────────

func normalizeStatus(raw string) string {
	switch strings.TrimSpace(raw) {
	case "مستمرة", "مستمر", "قادم قريبًا":
		return "ongoing"
	case "مكتمل", "مكتملة":
		return "completed"
	case "متوقف", "متوقفة":
		return "hiatus"
	default:
		return strings.TrimSpace(raw)
	}
}

func resolveURL(base, raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.HasPrefix(raw, "data:") {
		return ""
	}
	ref, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	if ref.IsAbs() {
		return ref.String()
	}
	baseParsed, err := url.Parse(base)
	if err != nil {
		return raw
	}
	return baseParsed.ResolveReference(ref).String()
}

func extractSlug(rawURL string) string {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	path := rawURL
	if err == nil && u.Path != "" {
		path = u.Path
	}
	path = strings.TrimRight(path, "/")
	if idx := strings.LastIndex(path, "/"); idx != -1 {
		return path[idx+1:]
	}
	return path
}
