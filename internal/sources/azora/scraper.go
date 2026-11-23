package azora

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"

	"github.com/0xrinful/InkFetch/internal/models"
)

const (
	defaultPerPage   = 18
	maxSkippedPages  = 5
)

func (s *Source) Latest(ctx context.Context, page, size int) ([]models.Manga, error) {
	return s.queryMangas(ctx, "", "lastChapterAddedAt", page, size)
}

func (s *Source) Search(ctx context.Context, query string, page, size int) ([]models.Manga, error) {
	return s.queryMangas(ctx, strings.TrimSpace(query), "", page, size)
}

func (s *Source) queryMangas(
	ctx context.Context,
	query, orderBy string,
	page, size int,
) ([]models.Manga, error) {
	if page < 1 {
		page = 1
	}
	if size <= 0 {
		size = defaultPerPage
	}

	for skipped := 0; skipped <= maxSkippedPages; skipped++ {
		currentPage := page + skipped
		params := url.Values{
			"page":       {strconv.Itoa(currentPage)},
			"perPage":    {strconv.Itoa(size)},
			"searchTerm": {query},
		}
		if orderBy != "" {
			params.Set("orderBy", orderBy)
		}

		u := fmt.Sprintf("%s/api/query?%s", s.apiURL, params.Encode())

		resp, err := s.req.Get(ctx, u)
		if err != nil {
			return nil, fmt.Errorf("azora: query: %w", err)
		}

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			resp.Body.Close()
			return nil, fmt.Errorf("azora: query: status %d", resp.StatusCode)
		}

		var result searchResponse
		err = json.NewDecoder(resp.Body).Decode(&result)
		resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("azora: parse query: %w", err)
		}

		var mangas []models.Manga
		for _, p := range result.Posts {
			if p.isNovelPost() {
				continue
			}
			mangas = append(mangas, models.Manga{
				Slug:     p.Slug,
				SourceID: sourceID,
				Title:    strings.TrimSpace(p.Title),
				CoverURL: p.FeaturedImage,
			})
		}

		hasNextPage := result.TotalCount > currentPage*size
		if len(mangas) > 0 || !hasNextPage || len(result.Posts) == 0 {
			return mangas, nil
		}
	}

	return nil, nil
}

func (s *Source) Manga(ctx context.Context, slug string) (*models.Manga, error) {
	u := fmt.Sprintf("%s/api/post?postSlug=%s", s.apiURL, url.QueryEscape(slug))

	resp, err := s.req.Get(ctx, u)
	if err != nil {
		return nil, fmt.Errorf("azora: manga: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("azora: manga: status %d", resp.StatusCode)
	}

	var result postResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("azora: parse manga: %w", err)
	}

	p := result.Post
	if p.isNovelPost() {
		return nil, fmt.Errorf("azora: novels are unsupported")
	}

	manga := &models.Manga{
		Slug:        p.Slug,
		SourceID:    sourceID,
		Title:       strings.TrimSpace(p.Title),
		Description: formatDescription(p.Description, p.AlternativeTitles),
		CoverURL:    p.FeaturedImage,
		Status:      normalizeStatus(p.SeriesStatus),
		Genres:      collectGenres(p.SeriesType, p.Genres),
	}
	if manga.Slug == "" {
		manga.Slug = slug
	}

	rawChapters := p.Chapters
	// Iken truncates post.chapters on long series and exposes totalChapterCount;
	// fetch the full list from /api/chapters?postId= when truncated.
	if result.TotalChapterCount != nil && *result.TotalChapterCount > len(rawChapters) && p.ID > 0 {
		if fullChapters, err := s.fetchAllChapters(ctx, p.ID); err == nil && len(fullChapters) > len(rawChapters) {
			rawChapters = fullChapters
		}
	}

	for _, c := range rawChapters {
		if c.locked() {
			continue
		}

		releasedAt, _ := time.Parse(time.RFC3339Nano, c.CreatedAt)

		manga.Chapters = append(manga.Chapters, models.Chapter{
			SourceID:   sourceID,
			MangaSlug:  manga.Slug,
			Title:      strings.TrimSpace(c.Title),
			Number:     float64(c.Number),
			URL:        strconv.Itoa(c.ID),
			ReleasedAt: releasedAt.UTC(),
		})
	}

	return manga, nil
}

func (s *Source) fetchAllChapters(ctx context.Context, postID int) ([]chapterInList, error) {
	u := fmt.Sprintf("%s/api/chapters?postId=%d", s.apiURL, postID)

	resp, err := s.req.Get(ctx, u)
	if err != nil {
		return nil, fmt.Errorf("azora: chapters: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("azora: chapters: status %d", resp.StatusCode)
	}

	var result chaptersResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("azora: parse chapters: %w", err)
	}

	return result.Post.Chapters, nil
}

func (s *Source) Chapters(ctx context.Context, slug string) ([]models.Chapter, error) {
	manga, err := s.Manga(ctx, slug)
	if err != nil {
		return nil, err
	}
	return manga.Chapters, nil
}

func (s *Source) Pages(ctx context.Context, chapterURL string) ([]models.Page, error) {
	chapterID := strings.TrimSpace(chapterURL)
	if idx := strings.LastIndex(chapterID, "#"); idx != -1 {
		chapterID = chapterID[idx+1:]
	}

	u := fmt.Sprintf("%s/api/chapter?chapterId=%s", s.apiURL, url.QueryEscape(chapterID))

	resp, err := s.req.Get(ctx, u)
	if err != nil {
		return nil, fmt.Errorf("azora: pages: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("azora: pages: status %d", resp.StatusCode)
	}

	var result chapterResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("azora: parse pages: %w", err)
	}

	ch := result.Chapter

	if ch.IsShortLinkLocked {
		return nil, fmt.Errorf("azora: chapter locked (short link)")
	}
	if ch.IsLockedByCoins {
		return nil, fmt.Errorf("azora: chapter locked (coins required)")
	}
	if ch.IsPermanentlyLocked {
		return nil, fmt.Errorf("azora: chapter permanently locked")
	}

	images := make([]pageImage, len(ch.Images))
	copy(images, ch.Images)
	sort.SliceStable(images, func(i, j int) bool {
		oi, oj := math.MaxInt, math.MaxInt
		if images[i].Order != nil {
			oi = *images[i].Order
		}
		if images[j].Order != nil {
			oj = *images[j].Order
		}
		return oi < oj
	})

	pages := make([]models.Page, 0, len(images))
	for _, img := range images {
		rawURL := strings.TrimSpace(img.URL)
		if rawURL == "" {
			continue
		}
		pages = append(pages, models.Page{
			Number: len(pages) + 1,
			URL:    strings.ReplaceAll(rawURL, " ", "%20"),
		})
	}

	return pages, nil
}

func formatDescription(rawHTML, altTitles string) string {
	var parts []string

	if strings.TrimSpace(rawHTML) != "" {
		doc, err := goquery.NewDocumentFromReader(
			strings.NewReader(strings.ReplaceAll(rawHTML, "\n", "<br>")),
		)
		if err == nil {
			if text := strings.TrimSpace(doc.Text()); text != "" {
				parts = append(parts, text)
			}
		} else {
			parts = append(parts, strings.TrimSpace(rawHTML))
		}
	}

	if alt := strings.TrimSpace(altTitles); alt != "" {
		parts = append(parts, "Alternative Names: "+alt)
	}

	return strings.Join(parts, "\n\n")
}

func normalizeStatus(raw string) string {
	switch strings.ToUpper(strings.TrimSpace(raw)) {
	case "ONGOING", "COMING_SOON", "MASS_RELEASED":
		return "ongoing"
	case "COMPLETED":
		return "completed"
	case "CANCELLED", "CANCELED", "DROPPED":
		return "cancelled"
	case "HIATUS", "ON_HOLD":
		return "hiatus"
	default:
		return strings.ToLower(strings.TrimSpace(raw))
	}
}

func collectGenres(seriesType string, genres []genre) []string {
	seen := make(map[string]struct{}, len(genres)+1)
	out := make([]string, 0, len(genres)+1)

	add := func(name string) {
		name = strings.TrimSpace(name)
		if name == "" {
			return
		}
		key := strings.ToLower(name)
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		out = append(out, name)
	}

	switch strings.ToUpper(strings.TrimSpace(seriesType)) {
	case "MANGA":
		add("Manga")
	case "MANHUA":
		add("Manhua")
	case "MANHWA":
		add("Manhwa")
	}

	for _, g := range genres {
		add(g.Name)
	}

	return out
}
