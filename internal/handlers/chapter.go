package handlers

import (
	"errors"
	"html/template"
	"net/http"
	"strconv"

	"github.com/0xrinful/InkFetch/internal/models"
	"github.com/0xrinful/InkFetch/internal/service"
)

type Chapter struct {
	svc       *service.Service
	templates map[string]*template.Template
}

type chapterRowRenderData struct {
	SourceID  string
	MangaSlug string
	Chapter   models.ChapterRecord
	IsRead    bool
}

func NewChapter(svc *service.Service, templates map[string]*template.Template) *Chapter {
	return &Chapter{svc: svc, templates: templates}
}

func (c *Chapter) Download(w http.ResponseWriter, r *http.Request) {
	noSwap(w)

	chapter, _, err := c.lookupChapter(r)
	if err != nil {
		writeActionError(w, err)
		return
	}

	sourceID := r.PathValue("sourceID")
	if _, err := c.svc.DownloadChapter(r.Context(), sourceID, chapter.Chapter); err != nil {
		writeActionError(w, err)
		return
	}

	writeToast(w, "Chapter queued for download", "success")
	w.WriteHeader(http.StatusOK)
}

func (c *Chapter) Optimize(w http.ResponseWriter, r *http.Request) {
	noSwap(w)

	chapter, _, err := c.lookupChapter(r)
	if err != nil {
		writeActionError(w, err)
		return
	}

	if _, err := c.svc.OptimizeChapter(r.Context(), chapter.Chapter); err != nil {
		writeActionError(w, err)
		return
	}

	writeToast(w, "Chapter queued for optimization", "success")
	w.WriteHeader(http.StatusOK)
}

func (c *Chapter) Pack(w http.ResponseWriter, r *http.Request) {
	noSwap(w)

	chapter, _, err := c.lookupChapter(r)
	if err != nil {
		writeActionError(w, err)
		return
	}

	if _, err := c.svc.PackChapter(r.Context(), chapter.Chapter); err != nil {
		writeActionError(w, err)
		return
	}

	writeToast(w, "Chapter queued for CBZ packing", "success")
	w.WriteHeader(http.StatusOK)
}

func (c *Chapter) ToggleRead(w http.ResponseWriter, r *http.Request) {
	chapter, readMarks, err := c.lookupChapter(r)
	if err != nil {
		writeActionError(w, err)
		return
	}

	userID := getUserID(r.Context())
	sourceID := r.PathValue("sourceID")
	slug := r.PathValue("slug")
	number := chapter.Number

	nowRead := !readMarks[number]
	message := "Marked as read"
	if !nowRead {
		if err := c.svc.MarkUnread(r.Context(), userID, slug, sourceID, number); err != nil {
			writeActionError(w, err)
			return
		}
		message = "Marked as unread"
	} else {
		if err := c.svc.MarkRead(r.Context(), userID, slug, sourceID, number); err != nil {
			writeActionError(w, err)
			return
		}
	}

	writeToast(w, message, "success")
	renderTemplateName(w, c.templates, "manga.html", "chapter-row", chapterRowRenderData{
		SourceID:  sourceID,
		MangaSlug: slug,
		Chapter:   *chapter,
		IsRead:    nowRead,
	})
}

func (c *Chapter) lookupChapter(r *http.Request) (*models.ChapterRecord, map[float64]bool, error) {
	userID := getUserID(r.Context())
	sourceID := r.PathValue("sourceID")
	slug := r.PathValue("slug")
	rawNum := r.PathValue("num")
	number, err := strconv.ParseFloat(rawNum, 64)
	if err != nil {
		return nil, nil, err
	}

	page, err := c.svc.MangaPage(r.Context(), userID, slug, sourceID)
	if err != nil {
		return nil, nil, err
	}

	readMarks := make(map[float64]bool, len(page.ReadMarks))
	for _, read := range page.ReadMarks {
		readMarks[read] = true
	}

	for i := range page.Chapters {
		if page.Chapters[i].Number == number {
			return &page.Chapters[i], readMarks, nil
		}
	}

	return nil, readMarks, errors.New("chapter not found")
}
