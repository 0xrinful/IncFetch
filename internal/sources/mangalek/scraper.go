package mangalek

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/PuerkitoBio/goquery"
	"golang.org/x/sync/errgroup"

	"github.com/0xrinful/InkFetch/internal/helpers/parse"
	"github.com/0xrinful/InkFetch/internal/models"
	"github.com/0xrinful/InkFetch/internal/requester"
)

const (
	defaultPageSize = 25
	ajaxTemplate    = "madara-core/content/content-archive"
)

var (
	urlRe             = regexp.MustCompile(`^https?://[^\s/$.?#].[^\s]*$`)
	imageDescriptorRe = regexp.MustCompile(`^(\d+|\d+\.\d+)[wx]$`)
	updatingRe        = regexp.MustCompile(`(?i)Updating|Atualizando`)
	completedStatuses = []string{
		"completed", "completo", "completado", "concluído", "concluido", "finalizado",
		"achevé", "terminé", "hoàn thành", "مكتملة", "مكتمل", "已完结", "tamamlandı",
		"đã hoàn thành", "завершено", "tamamlanan", "complété",
	}
	ongoingStatuses = []string{
		"ongoing", "on going", "updating", "продолжается", "em lançamento", "em andamento",
		"en cours", "ativo", "lançando", "đang tiến hành", "còn nữa", "devam ediyor",
		"in corso", "in arrivo", "مستمرة", "مستمر", "en curso", "emision", "curso",
		"en marcha", "publicandose", "publicándose", "en emision", "连载中", "đang làm",
		"em postagem", "devam eden", "em progresso", "atualizações semanais", "виходить",
		"đang ra",
	}
	hiatusStatuses = []string{
		"on hold", "hiatus", "pausado", "en espera", "durduruldu", "beklemede",
		"đang chờ", "متوقف", "en pause", "заморожено", "en attente", "tạm dừng",
	}
	cancelledStatuses = []string{
		"canceled", "cancelled", "cancelado", "iptal edildi", "đã hủy", "ملغي",
		"abandonné", "заброшено", "annulé",
	}
)

type browseMode int

const (
	modeLatest browseMode = iota
	modeSearch
)

// ─── Latest & Search (Madara AJAX) ───────────────────────────────────────────

func (s *Source) Latest(ctx context.Context, page, size int) ([]models.Manga, error) {
	return s.ajaxList(ctx, page, size, modeLatest, "", false)
}

func (s *Source) Search(ctx context.Context, query string, page, size int) ([]models.Manga, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return s.ajaxList(ctx, page, size, modeSearch, "", false)
	}

	// WordPress search skips alternative names stored in _wp_manga_alternative post meta.
	var (
		primary []models.Manga
		alt     []models.Manga
	)

	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		var err error
		primary, err = s.ajaxList(gctx, page, size, modeSearch, query, false)
		return err
	})
	g.Go(func() error {
		var err error
		alt, err = s.ajaxList(gctx, page, size, modeSearch, query, true)
		if err != nil {
			// Best-effort alt-name search: don't fail the whole search if it errors.
			alt = nil
		}
		return nil
	})
	if err := g.Wait(); err != nil {
		return nil, err
	}

	seen := make(map[string]struct{}, len(primary)+len(alt))
	merged := make([]models.Manga, 0, len(primary)+len(alt))
	for _, list := range [][]models.Manga{primary, alt} {
		for _, m := range list {
			if _, ok := seen[m.Slug]; ok {
				continue
			}
			seen[m.Slug] = struct{}{}
			merged = append(merged, m)
		}
	}

	return merged, nil
}

func (s *Source) ajaxList(
	ctx context.Context,
	page, size int,
	mode browseMode,
	query string,
	byAltName bool,
) ([]models.Manga, error) {
	if page < 1 {
		page = 1
	}
	if size <= 0 {
		size = defaultPageSize
	}

	// Mangalek overrides filterNonMangaItems = false, so no _wp_manga_chapter_type meta_query.
	form := url.Values{
		"action":                           {"madara_load_more"},
		"page":                             {strconv.Itoa(page - 1)},
		"template":                         {ajaxTemplate},
		"vars[paged]":                      {"1"},
		"vars[template]":                   {"archive"},
		"vars[posts_per_page]":             {strconv.Itoa(size)},
		"vars[post_type]":                  {"wp-manga"},
		"vars[post_status]":                {"publish"},
		"vars[manga_archives_item_layout]": {"big_thumbnail"},
	}

	if mode == modeLatest {
		form.Set("vars[orderby]", "meta_value_num")
		form.Set("vars[meta_key]", "_latest_update")
		form.Set("vars[order]", "DESC")
	}

	if byAltName {
		form.Set("vars[meta_query][alternative][key]", "_wp_manga_alternative")
		form.Set("vars[meta_query][alternative][value]", query)
		form.Set("vars[meta_query][alternative][compare]", "LIKE")
	} else if query != "" {
		form.Set("vars[s]", query)
	}

	ajaxURL := s.baseURL + "/wp-admin/admin-ajax.php"
	resp, err := s.postXHR(ctx, ajaxURL, s.baseURL+"/", form)
	if err != nil {
		return nil, fmt.Errorf("mangalek: ajax list: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("mangalek: ajax list: status %d", resp.StatusCode)
	}

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("mangalek: parse ajax list: %w", err)
	}

	return s.parseArchive(doc), nil
}

func (s *Source) parseArchive(doc *goquery.Document) []models.Manga {
	seen := make(map[string]struct{})
	var mangas []models.Manga

	doc.Find("div.page-item-detail, .manga__item, .c-tabs-item__content").
		Each(func(_ int, sel *goquery.Selection) {
			a := sel.Find(".post-title a").First()
			if a.Length() == 0 {
				return
			}

			href := resolveURL(s.baseURL, a.AttrOr("href", ""))
			if href == "" {
				return
			}

			slug := extractSlug(href)
			if slug == "" {
				return
			}
			if _, dup := seen[slug]; dup {
				return
			}
			seen[slug] = struct{}{}

			title := strings.TrimSpace(a.Text())
			coverURL := imageFromElement(sel.Find("img").First(), s.baseURL)

			mangas = append(mangas, models.Manga{
				Slug:     slug,
				SourceID: sourceID,
				Title:    title,
				CoverURL: coverURL,
			})
		})

	return mangas
}

// ─── Manga Detail & Chapters ─────────────────────────────────────────────────

func (s *Source) Manga(ctx context.Context, slug string) (*models.Manga, error) {
	mangaURL := fmt.Sprintf("%s/manga/%s/", s.baseURL, strings.Trim(slug, "/"))

	resp, err := s.req.Get(ctx, mangaURL)
	if err != nil {
		return nil, fmt.Errorf("mangalek: manga: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("mangalek: manga: status %d", resp.StatusCode)
	}

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("mangalek: parse manga: %w", err)
	}

	resolvedURL := mangaURL
	if resp.Request != nil && resp.Request.URL != nil {
		resolvedURL = resp.Request.URL.String()
	}

	manga := s.parseMangaDetails(doc, slug, resolvedURL)

	// Mangalek uses ChapterMode.AdminAjax (POST /wp-admin/admin-ajax.php action=manga_get_chapters).
	chapters, err := s.fetchChapters(ctx, doc, slug, resolvedURL)
	if err != nil {
		return nil, err
	}
	manga.Chapters = chapters

	return manga, nil
}

func (s *Source) parseMangaDetails(doc *goquery.Document, slug, pageURL string) *models.Manga {
	manga := &models.Manga{
		Slug:     slug,
		SourceID: sourceID,
	}

	titleSel := doc.Find("div.post-title h3, div.post-title h1, #manga-title > h1").First()
	manga.Title = ownText(titleSel)
	if manga.Title == "" {
		manga.Title = strings.TrimSpace(titleSel.Text())
	}

	manga.Description = extractDescription(doc)
	manga.CoverURL = imageFromElement(doc.Find("div.summary_image img").First(), pageURL)

	statusText := extractStatusText(doc)
	manga.Status = normalizeStatus(statusText)

	seenGenres := make(map[string]struct{})
	addGenre := func(g string) {
		g = strings.TrimSpace(g)
		if g == "" || updatingRe.MatchString(g) {
			return
		}
		key := strings.ToLower(g)
		if _, ok := seenGenres[key]; ok {
			return
		}
		seenGenres[key] = struct{}{}
		manga.Genres = append(manga.Genres, g)
	}

	doc.Find("div.genres-content a").Each(func(_ int, sel *goquery.Selection) {
		addGenre(sel.Text())
	})
	doc.Find("div.tags-content a").Each(func(_ int, sel *goquery.Selection) {
		addGenre(sel.Text())
	})
	doc.Find(".post-content_item").Each(func(_ int, sel *goquery.Selection) {
		if strings.Contains(strings.ToLower(sel.Text()), "type") {
			addGenre(sel.Find(".summary-content").First().Text())
		}
	})

	return manga
}

func (s *Source) fetchChapters(
	ctx context.Context,
	doc *goquery.Document,
	slug, mangaURL string,
) ([]models.Chapter, error) {
	// Primary: ChapterMode.AdminAjax (matching Keiyoushi Mangalek.kt)
	if postID := extractMangaID(doc); postID != "" {
		chapters, err := s.fetchAdminAjaxChapters(ctx, slug, mangaURL, postID)
		if err == nil && len(chapters) > 0 {
			return chapters, nil
		}
		// If AdminAjax failed and inline chapters exist, fall back before returning error.
		inline := s.parseChapterList(doc, slug, mangaURL)
		if len(inline) > 0 {
			return inline, nil
		}
		if err != nil {
			return nil, err
		}
	}

	// Fallback 1: inline chapters on the manga page
	if inline := s.parseChapterList(doc, slug, mangaURL); len(inline) > 0 {
		return inline, nil
	}

	// Fallback 2: ChapterMode.MangaAjax ({mangaURL}/ajax/chapters/)
	ajaxURL := strings.TrimRight(mangaURL, "/") + "/ajax/chapters/"
	resp, err := s.postXHR(ctx, ajaxURL, mangaURL, url.Values{})
	if err != nil {
		return nil, nil
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, nil
	}

	chDoc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return nil, nil
	}

	return s.parseChapterList(chDoc, slug, mangaURL), nil
}

func (s *Source) fetchAdminAjaxChapters(
	ctx context.Context,
	slug, mangaURL, postID string,
) ([]models.Chapter, error) {
	form := url.Values{
		"action": {"manga_get_chapters"},
		"manga":  {postID},
	}

	ajaxURL := s.baseURL + "/wp-admin/admin-ajax.php"
	resp, err := s.postXHR(ctx, ajaxURL, mangaURL, form)
	if err != nil {
		return nil, fmt.Errorf("mangalek: admin-ajax chapters: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("mangalek: admin-ajax chapters: status %d", resp.StatusCode)
	}

	chDoc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("mangalek: parse admin-ajax chapters: %w", err)
	}

	return s.parseChapterList(chDoc, slug, mangaURL), nil
}

func (s *Source) parseChapterList(
	doc *goquery.Document,
	slug, pageURL string,
) []models.Chapter {
	var chapters []models.Chapter

	doc.Find("li.wp-manga-chapter").Each(func(_ int, sel *goquery.Selection) {
		a := sel.Find("a").First()
		href := resolveURL(pageURL, a.AttrOr("href", ""))
		if href == "" {
			return
		}

		title := strings.TrimSpace(a.Text())
		number, err := parse.ParseChapterNumber(title)
		if err != nil {
			number, _ = parse.ParseChapterNumber(extractSlug(href))
		}

		dateText := extractChapterDateText(sel)
		var releasedAt models.Chapter
		if parsed, err := parse.ParseDate(dateText); err == nil {
			releasedAt.ReleasedAt = parsed.UTC()
		}

		chapters = append(chapters, models.Chapter{
			SourceID:   sourceID,
			MangaSlug:  slug,
			URL:        href,
			Title:      title,
			Number:     number,
			ReleasedAt: releasedAt.ReleasedAt,
		})
	})

	return chapters
}

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
		return nil, fmt.Errorf("mangalek: empty chapter url")
	}

	doc, resolvedURL, err := s.fetchDocument(ctx, targetURL)
	if err != nil {
		return nil, fmt.Errorf("mangalek: pages: %w", err)
	}

	// If chapter is in paged mode (#single-pager), re-fetch with ?style=list (MadaraBase.kt).
	if doc.Find("#single-pager").Length() > 0 {
		if u, parseErr := url.Parse(resolvedURL); parseErr == nil {
			q := u.Query()
			q.Set("style", "list")
			u.RawQuery = q.Encode()
			if listDoc, listURL, listErr := s.fetchDocument(ctx, u.String()); listErr == nil {
				doc = listDoc
				resolvedURL = listURL
			}
		}
	}

	return s.parsePages(doc, resolvedURL)
}

func (s *Source) fetchDocument(ctx context.Context, targetURL string) (*goquery.Document, string, error) {
	resp, err := s.req.Get(ctx, targetURL)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", fmt.Errorf("status %d", resp.StatusCode)
	}

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return nil, "", err
	}

	resolvedURL := targetURL
	if resp.Request != nil && resp.Request.URL != nil {
		resolvedURL = resp.Request.URL.String()
	}

	return doc, resolvedURL, nil
}

func (s *Source) parsePages(doc *goquery.Document, pageURL string) ([]models.Page, error) {
	if protector := doc.Find("#chapter-protector-data").First(); protector.Length() > 0 {
		urls, err := parseProtectedPages(protector)
		if err != nil {
			return nil, fmt.Errorf("mangalek: decrypt pages: %w", err)
		}
		pages := make([]models.Page, 0, len(urls))
		for _, raw := range urls {
			imgURL := resolveURL(pageURL, raw)
			if imgURL == "" {
				continue
			}
			pages = append(pages, models.Page{
				Number: len(pages) + 1,
				URL:    imgURL,
			})
		}
		return pages, nil
	}

	var pages []models.Page
	addImg := func(img *goquery.Selection) {
		if img.Length() == 0 {
			return
		}
		imgURL := imageFromElement(img, pageURL)
		if imgURL == "" {
			return
		}
		pages = append(pages, models.Page{
			Number: len(pages) + 1,
			URL:    imgURL,
		})
	}

	// Matches MadaraBase.kt pageListParseSelector:
	// "div.page-break, li.blocks-gallery-item, .reading-content .text-left:not(:has(.blocks-gallery-item))"
	doc.Find("div.page-break, li.blocks-gallery-item, .reading-content .text-left").
		Each(func(_ int, sel *goquery.Selection) {
			if sel.HasClass("text-left") && sel.Find(".blocks-gallery-item").Length() > 0 {
				return
			}
			addImg(sel.Find("img").First())
		})

	if len(pages) == 0 {
		doc.Find("img.wp-manga-chapter-img").Each(func(_ int, sel *goquery.Selection) {
			addImg(sel)
		})
	}

	return pages, nil
}

type chapterProtectorData struct {
	Ciphertext string `json:"ct"`
	Salt       string `json:"s"`
}

func parseProtectedPages(protector *goquery.Selection) ([]string, error) {
	script := protector.Text()
	if src := protector.AttrOr("src", ""); strings.HasPrefix(src, "data:text/javascript;base64,") {
		encoded := strings.TrimPrefix(src, "data:text/javascript;base64,")
		decoded, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return nil, fmt.Errorf("decode protector script: %w", err)
		}
		script = string(decoded)
	}

	password := substringBetween(script, "wpmangaprotectornonce='", "';")
	encrypted := strings.ReplaceAll(substringBetween(script, "chapter_data='", "';"), `\/`, `/`)
	if password == "" || encrypted == "" {
		return nil, fmt.Errorf("missing protector nonce or chapter_data")
	}

	var data chapterProtectorData
	if err := json.Unmarshal([]byte(encrypted), &data); err != nil {
		return nil, fmt.Errorf("parse chapter_data json: %w", err)
	}

	raw, err := decryptChapterData(data, password)
	if err != nil {
		return nil, err
	}

	var inner string
	if err := json.Unmarshal([]byte(raw), &inner); err == nil {
		var urls []string
		if err := json.Unmarshal([]byte(inner), &urls); err == nil {
			return urls, nil
		}
	}

	var urls []string
	if err := json.Unmarshal([]byte(raw), &urls); err != nil {
		return nil, fmt.Errorf("parse decrypted page list: %w", err)
	}
	return urls, nil
}

func decryptChapterData(data chapterProtectorData, password string) (string, error) {
	salt, err := hex.DecodeString(data.Salt)
	if err != nil {
		return "", fmt.Errorf("decode salt: %w", err)
	}

	ciphertext, err := base64.StdEncoding.DecodeString(data.Ciphertext)
	if err != nil {
		return "", fmt.Errorf("decode ciphertext: %w", err)
	}
	if len(ciphertext) == 0 || len(ciphertext)%aes.BlockSize != 0 {
		return "", fmt.Errorf("invalid ciphertext length %d", len(ciphertext))
	}

	// OpenSSL EVP_BytesToKey with MD5: derive 32-byte key + 16-byte IV.
	passwordBytes := []byte(password)
	keyAndIV := make([]byte, 0, 48)
	var prev []byte
	for len(keyAndIV) < 48 {
		h := md5.New()
		h.Write(prev)
		h.Write(passwordBytes)
		h.Write(salt)
		prev = h.Sum(nil)
		keyAndIV = append(keyAndIV, prev...)
	}

	block, err := aes.NewCipher(keyAndIV[:32])
	if err != nil {
		return "", err
	}

	plain := make([]byte, len(ciphertext))
	cipher.NewCBCDecrypter(block, keyAndIV[32:48]).CryptBlocks(plain, ciphertext)

	pad := int(plain[len(plain)-1])
	if pad == 0 || pad > aes.BlockSize || pad > len(plain) {
		return "", fmt.Errorf("invalid pkcs7 padding")
	}
	for i := len(plain) - pad; i < len(plain); i++ {
		if int(plain[i]) != pad {
			return "", fmt.Errorf("invalid pkcs7 padding")
		}
	}

	return string(plain[:len(plain)-pad]), nil
}

// ─── HTTP & DOM Helpers ──────────────────────────────────────────────────────

func (s *Source) postXHR(
	ctx context.Context,
	targetURL, referer string,
	form url.Values,
) (*http.Response, error) {
	headers := map[string]string{
		"Content-Type":     "application/x-www-form-urlencoded",
		"X-Requested-With": "XMLHttpRequest",
	}
	if referer != "" {
		headers["Referer"] = referer
	}

	return s.req.Do(ctx, targetURL, requester.RequestOptions{
		Method:  http.MethodPost,
		Mode:    requester.ModeDefault,
		Headers: headers,
		Body:    []byte(form.Encode()),
	})
}

func extractMangaID(doc *goquery.Document) string {
	if id := strings.TrimSpace(doc.Find("[id^=manga-chapters-holder]").First().AttrOr("data-id", "")); id != "" {
		return id
	}
	if id := strings.TrimSpace(doc.Find("input.rating-post-id").First().AttrOr("value", "")); id != "" {
		return id
	}
	if id := strings.TrimSpace(doc.Find("a[data-post]").First().AttrOr("data-post", "")); id != "" {
		return id
	}
	if href := doc.Find("link[rel=shortlink]").First().AttrOr("href", ""); strings.Contains(href, "?p=") {
		id := strings.SplitN(strings.SplitN(href, "?p=", 2)[1], "&", 2)[0]
		if id = strings.TrimSpace(id); id != "" {
			return id
		}
	}
	return ""
}

func extractDescription(doc *goquery.Document) string {
	el := doc.Find("div.description-summary div.summary__content, div.summary_content div.post-content_item > h5 + div, div.summary_content div.manga-excerpt").First()
	if el.Length() == 0 {
		return ""
	}

	var paragraphs []string
	el.Find("p").Each(func(_ int, sel *goquery.Selection) {
		if t := strings.TrimSpace(sel.Text()); t != "" {
			paragraphs = append(paragraphs, t)
		}
	})
	if len(paragraphs) > 0 {
		return strings.Join(paragraphs, "\n\n")
	}
	return strings.TrimSpace(el.Text())
}

func extractStatusText(doc *goquery.Document) string {
	var status string
	doc.Find("div.summary-heading").Each(func(_ int, sel *goquery.Selection) {
		if strings.Contains(strings.ToLower(sel.Text()), "status") {
			if next := sel.Next(); next.Length() > 0 && next.Is("div") {
				status = strings.TrimSpace(next.Text())
			}
		}
	})
	if status != "" {
		return status
	}
	return strings.TrimSpace(doc.Find("div.summary-content").Last().Text())
}

func normalizeStatus(raw string) string {
	lower := strings.ToLower(strings.TrimSpace(raw))
	if lower == "" {
		return ""
	}
	for _, s := range completedStatuses {
		if strings.Contains(lower, s) {
			return "completed"
		}
	}
	for _, s := range ongoingStatuses {
		if strings.Contains(lower, s) {
			return "ongoing"
		}
	}
	for _, s := range hiatusStatuses {
		if strings.Contains(lower, s) {
			return "hiatus"
		}
	}
	for _, s := range cancelledStatuses {
		if strings.Contains(lower, s) {
			return "cancelled"
		}
	}
	return strings.TrimSpace(raw)
}

func extractChapterDateText(sel *goquery.Selection) string {
	if alt := strings.TrimSpace(sel.Find("img:not(.thumb)").First().AttrOr("alt", "")); alt != "" {
		return alt
	}
	if title := strings.TrimSpace(sel.Find("span a").First().AttrOr("title", "")); title != "" {
		return title
	}
	return strings.TrimSpace(sel.Find("span.chapter-release-date").First().Text())
}

func imageFromElement(el *goquery.Selection, base string) string {
	if el.Length() == 0 {
		return ""
	}

	for _, attr := range []string{
		"data-src",
		"data-lazy-src",
		"data-lzl-src",
		"data-cfsrc",
		"data-manga-src",
	} {
		if val, ok := el.Attr(attr); ok {
			if resolved := resolveURL(base, val); resolved != "" {
				return resolved
			}
		}
	}

	if srcset, ok := el.Attr("srcset"); ok {
		if best := getSrcSetImage(srcset, base); best != "" {
			return best
		}
	}

	if src, ok := el.Attr("src"); ok {
		return resolveURL(base, src)
	}

	return ""
}

func getSrcSetImage(srcset, base string) string {
	var (
		bestURL     string
		bestDesc    float64 = -1
		fallbackURL string
	)

	for _, part := range strings.Split(srcset, ",") {
		fields := strings.Fields(strings.TrimSpace(part))
		if len(fields) == 0 {
			continue
		}
		candidateURL := resolveURL(base, fields[0])
		if !urlRe.MatchString(candidateURL) {
			continue
		}
		if fallbackURL == "" || candidateURL > fallbackURL {
			fallbackURL = candidateURL
		}
		if len(fields) >= 2 {
			if m := imageDescriptorRe.FindStringSubmatch(fields[1]); len(m) == 2 {
				if val, err := strconv.ParseFloat(m[1], 64); err == nil && val > bestDesc {
					bestDesc = val
					bestURL = candidateURL
				}
			}
		}
	}

	if bestURL != "" {
		return bestURL
	}
	return fallbackURL
}

func ownText(sel *goquery.Selection) string {
	if sel.Length() == 0 {
		return ""
	}
	return strings.TrimSpace(sel.Contents().Not("*").Text())
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

func substringBetween(s, start, end string) string {
	idx := strings.Index(s, start)
	if idx == -1 {
		return ""
	}
	rest := s[idx+len(start):]
	endIdx := strings.Index(rest, end)
	if endIdx == -1 {
		return ""
	}
	return rest[:endIdx]
}
