package handlers

import (
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/0xrinful/InkFetch/internal/models"
	"github.com/0xrinful/InkFetch/internal/service"
)

type Manga struct {
	svc       *service.Service
	templates map[string]*template.Template
}

type RangeRequest struct {
	From  float64
	To    float64
	All   bool
	Force bool
}

func (req *RangeRequest) Parse(r *http.Request) error {
	if err := r.ParseForm(); err != nil {
		return err
	}

	fromStr := strings.TrimSpace(r.FormValue("from"))
	toStr := strings.TrimSpace(r.FormValue("to"))

	if fromStr != "" {
		from, err := strconv.ParseFloat(fromStr, 64)
		if err != nil {
			return fmt.Errorf("invalid start chapter: %w", err)
		}
		req.From = from
	}

	if toStr != "" {
		to, err := strconv.ParseFloat(toStr, 64)
		if err != nil {
			return fmt.Errorf("invalid end chapter: %w", err)
		}
		req.To = to
	}

	req.All = r.FormValue("all") == "on" || r.FormValue("all") == "true"
	req.Force = r.FormValue("force") == "on" || r.FormValue("force") == "true"

	if !req.All && fromStr == "" && toStr == "" {
		return errors.New("specify a chapter range or select all chapters")
	}
	return nil
}

type mangaPageData struct {
	CurrentPath     string
	SourceID        string
	SourceName      string
	MangaSlug       string
	Manga           models.MangaRecord
	Chapters        []models.ChapterRecord
	ReadMarks       map[float64]bool
	DownloadedCount int
	PackedCount     int
	ReadCount       int
	ContinueChapter float64
	HasContinue     bool
}

func NewManga(svc *service.Service, templates map[string]*template.Template) *Manga {
	return &Manga{svc: svc, templates: templates}
}

func (m *Manga) Detail(w http.ResponseWriter, r *http.Request) {
	userID := getUserID(r.Context())
	sourceID := r.PathValue("sourceID")
	slug := r.PathValue("slug")

	result, err := m.svc.MangaPage(r.Context(), userID, slug, sourceID)
	if err != nil {
		if errors.Is(err, service.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	chapters := append([]models.ChapterRecord(nil), result.Chapters...)
	sort.Slice(chapters, func(i, j int) bool {
		return chapters[i].Number > chapters[j].Number
	})

	readMarks := make(map[float64]bool, len(result.ReadMarks))
	for _, number := range result.ReadMarks {
		readMarks[number] = true
	}

	downloadedCount := 0
	packedCount := 0
	for _, ch := range chapters {
		if ch.Downloaded {
			downloadedCount++
		}
		if ch.Packed {
			packedCount++
		}
	}

	// Find best "Continue Reading" chapter: lowest-numbered unread downloaded chapter,
	// falling back to highest-numbered downloaded chapter.
	continueNum := 0.0
	hasContinue := false
	chaptersAsc := append([]models.ChapterRecord(nil), result.Chapters...)
	sort.Slice(chaptersAsc, func(i, j int) bool {
		return chaptersAsc[i].Number < chaptersAsc[j].Number
	})
	for _, ch := range chaptersAsc {
		if ch.Downloaded && !readMarks[ch.Number] {
			continueNum = ch.Number
			hasContinue = true
			break
		}
	}
	if !hasContinue {
		for _, ch := range chapters {
			if ch.Downloaded {
				continueNum = ch.Number
				hasContinue = true
				break
			}
		}
	}

	sourceName := sourceID
	if info, ok := m.svc.SourceInfo(sourceID); ok {
		sourceName = info.Name
	}

	renderTemplate(w, m.templates, "manga.html", mangaPageData{
		CurrentPath:     "library",
		SourceID:        sourceID,
		SourceName:      sourceName,
		MangaSlug:       slug,
		Manga:           *result.Manga,
		Chapters:        chapters,
		ReadMarks:       readMarks,
		DownloadedCount: downloadedCount,
		PackedCount:     packedCount,
		ReadCount:       len(readMarks),
		ContinueChapter: continueNum,
		HasContinue:     hasContinue,
	})
}

func (m *Manga) BulkAction(w http.ResponseWriter, r *http.Request) {
	noSwap(w)

	if err := r.ParseForm(); err != nil {
		writeActionError(w, err)
		return
	}

	action := strings.TrimSpace(r.FormValue("action"))
	rawNums := r.Form["chapters"]
	force := r.FormValue("force") == "on" || r.FormValue("force") == "true"

	if len(rawNums) == 0 {
		writeActionError(w, errors.New("select at least one chapter first"))
		return
	}

	nums := make([]float64, 0, len(rawNums))
	for _, raw := range rawNums {
		n, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
		if err != nil {
			continue
		}
		nums = append(nums, n)
	}
	if len(nums) == 0 {
		writeActionError(w, errors.New("no valid chapter numbers selected"))
		return
	}

	sourceID := r.PathValue("sourceID")
	slug := r.PathValue("slug")
	userID := getUserID(r.Context())

	switch action {
	case "download":
		queued, err := m.svc.DownloadChapters(r.Context(), sourceID, slug, nums, force)
		if err != nil {
			writeActionError(w, err)
			return
		}
		if queued == 0 {
			writeToast(w, "Selected chapters are already downloaded or queued", "info")
		} else {
			writeToast(w, fmt.Sprintf("Queued %d chapter download(s)", queued), "success")
		}

	case "optimize":
		queued, err := m.svc.OptimizeChapters(r.Context(), sourceID, slug, nums, force)
		if err != nil {
			writeActionError(w, err)
			return
		}
		if queued == 0 {
			writeToast(w, "No downloaded chapters needed optimization", "info")
		} else {
			writeToast(w, fmt.Sprintf("Queued %d optimize job(s)", queued), "success")
		}

	case "pack":
		queued, err := m.svc.PackChapters(r.Context(), sourceID, slug, nums, force)
		if err != nil {
			writeActionError(w, err)
			return
		}
		if queued == 0 {
			writeToast(w, "No downloaded chapters needed packing", "info")
		} else {
			writeToast(w, fmt.Sprintf("Queued %d pack job(s)", queued), "success")
		}

	case "read":
		if err := m.svc.MarkChaptersRead(r.Context(), userID, sourceID, slug, nums); err != nil {
			writeActionError(w, err)
			return
		}
		writeToast(w, fmt.Sprintf("Marked %d chapter(s) as read", len(nums)), "success")
		w.Header().Set("HX-Refresh", "true")

	case "unread":
		if err := m.svc.MarkChaptersUnread(r.Context(), userID, sourceID, slug, nums); err != nil {
			writeActionError(w, err)
			return
		}
		writeToast(w, fmt.Sprintf("Marked %d chapter(s) as unread", len(nums)), "success")
		w.Header().Set("HX-Refresh", "true")

	default:
		writeActionError(w, fmt.Errorf("unknown bulk action %q", action))
		return
	}

	w.WriteHeader(http.StatusOK)
}

func (m *Manga) Unfavorite(w http.ResponseWriter, r *http.Request) {
	sourceID := r.PathValue("sourceID")
	slug := r.PathValue("slug")
	userID := getUserID(r.Context())

	if err := m.svc.Unfavorite(r.Context(), userID, sourceID, slug); err != nil {
		writeActionError(w, err)
		return
	}

	w.Header().Set("HX-Redirect", "/")
	w.WriteHeader(http.StatusOK)
}

func (m *Manga) Cover(w http.ResponseWriter, r *http.Request) {
	userID := getUserID(r.Context())
	sourceID := r.PathValue("sourceID")
	slug := r.PathValue("slug")

	result, err := m.svc.MangaPage(r.Context(), userID, slug, sourceID)
	if err != nil {
		if errors.Is(err, service.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if result.Manga == nil || result.Manga.CoverPath == "" {
		http.NotFound(w, r)
		return
	}

	coverPath, err := m.svc.Files().ResolvePath(result.Manga.CoverPath)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	file, err := os.Open(coverPath)
	if err != nil {
		if os.IsNotExist(err) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if info.IsDir() {
		http.NotFound(w, r)
		return
	}

	switch strings.ToLower(filepath.Ext(coverPath)) {
	case ".png":
		w.Header().Set("Content-Type", "image/png")
	case ".webp":
		w.Header().Set("Content-Type", "image/webp")
	case ".gif":
		w.Header().Set("Content-Type", "image/gif")
	default:
		w.Header().Set("Content-Type", "image/jpeg")
	}
	w.Header().Set("Cache-Control", "public, max-age=86400")
	http.ServeContent(w, r, info.Name(), info.ModTime(), file)
}

func (m *Manga) Download(w http.ResponseWriter, r *http.Request) {
	noSwap(w)

	var req RangeRequest
	if err := req.Parse(r); err != nil {
		writeActionError(w, err)
		return
	}

	rangeReq := models.ChapterRange{From: req.From, To: req.To, All: req.All, Force: req.Force}
	sourceID := r.PathValue("sourceID")
	slug := r.PathValue("slug")
	jobIDs, err := m.svc.DownloadRange(r.Context(), sourceID, slug, rangeReq)
	if err != nil {
		writeActionError(w, err)
		return
	}

	if len(jobIDs) == 0 {
		writeToast(w, "All chapters in range are already downloaded or queued", "info")
	} else {
		writeToast(w, fmt.Sprintf("Queued %d chapter download(s)", len(jobIDs)), "success")
	}
	w.WriteHeader(http.StatusOK)
}

func (m *Manga) Optimize(w http.ResponseWriter, r *http.Request) {
	noSwap(w)

	var req RangeRequest
	if err := req.Parse(r); err != nil {
		writeActionError(w, err)
		return
	}

	rangeReq := models.ChapterRange{From: req.From, To: req.To, All: req.All, Force: req.Force}
	sourceID := r.PathValue("sourceID")
	slug := r.PathValue("slug")
	jobIDs, err := m.svc.OptimizeRange(r.Context(), sourceID, slug, rangeReq)
	if err != nil {
		writeActionError(w, err)
		return
	}

	if len(jobIDs) == 0 {
		writeToast(w, "No downloaded chapters needed optimization", "info")
	} else {
		writeToast(w, fmt.Sprintf("Queued %d optimize job(s)", len(jobIDs)), "success")
	}
	w.WriteHeader(http.StatusOK)
}

func (m *Manga) Pack(w http.ResponseWriter, r *http.Request) {
	noSwap(w)

	var req RangeRequest
	if err := req.Parse(r); err != nil {
		writeActionError(w, err)
		return
	}

	rangeReq := models.ChapterRange{From: req.From, To: req.To, All: req.All, Force: req.Force}
	sourceID := r.PathValue("sourceID")
	slug := r.PathValue("slug")
	jobIDs, err := m.svc.PackRange(r.Context(), sourceID, slug, rangeReq)
	if err != nil {
		writeActionError(w, err)
		return
	}

	if len(jobIDs) == 0 {
		writeToast(w, "No downloaded chapters needed packing", "info")
	} else {
		writeToast(w, fmt.Sprintf("Queued %d pack job(s)", len(jobIDs)), "success")
	}
	w.WriteHeader(http.StatusOK)
}

func (m *Manga) Refresh(w http.ResponseWriter, r *http.Request) {
	noSwap(w)

	sourceID := r.PathValue("sourceID")
	slug := r.PathValue("slug")
	if err := m.svc.RefreshManga(r.Context(), sourceID, slug); err != nil {
		writeActionError(w, err)
		return
	}

	writeToast(w, "Chapters synced", "success")
	w.Header().Set("HX-Refresh", "true")
	w.WriteHeader(http.StatusOK)
}

func (m *Manga) DownloadPacked(w http.ResponseWriter, r *http.Request) {
	var req RangeRequest
	if err := req.Parse(r); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	rangeReq := models.ChapterRange{From: req.From, To: req.To, All: req.All, Force: req.Force}
	sourceID := r.PathValue("sourceID")
	slug := r.PathValue("slug")

	destZip, err := m.svc.PackManga(r.Context(), sourceID, slug, rangeReq)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}

	defer func() {
		if err := os.Remove(destZip); err != nil {
			slog.Error("failed to remove temp zip", "name", destZip, "err", err)
		}
	}()

	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", slug+".zip"))
	w.Header().Set("Content-Type", "application/zip")

	http.ServeFile(w, r, destZip)
}

func (m *Manga) DeleteFiles(w http.ResponseWriter, r *http.Request) {
	noSwap(w)

	if err := r.ParseForm(); err != nil {
		writeActionError(w, err)
		return
	}

	req := service.DeleteRequest{
		Raw:       r.FormValue("raw") == "on",
		Optimized: r.FormValue("optimized") == "on",
		Packed:    r.FormValue("packed") == "on",
	}
	if !req.Raw && !req.Optimized && !req.Packed {
		writeActionError(w, errors.New("select at least one file tier to delete"))
		return
	}

	sourceID := r.PathValue("sourceID")
	slug := r.PathValue("slug")
	if err := m.svc.DeleteMangaFiles(sourceID, slug, req); err != nil {
		writeActionError(w, err)
		return
	}

	writeToast(w, "Selected files deleted", "success")
	w.Header().Set("HX-Refresh", "true")
	w.WriteHeader(http.StatusOK)
}
