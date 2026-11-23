package handlers

import (
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"strconv"
	"strings"

	"github.com/0xrinful/InkFetch/internal/models"
	"github.com/0xrinful/InkFetch/internal/service"
	"github.com/0xrinful/InkFetch/internal/sources"
)

type Sources struct {
	svc       *service.Service
	templates map[string]*template.Template
}

type sourceBrowseData struct {
	CurrentPath string
	Sources     []sources.SourceInfo
	SourceID    string
	SourceName  string
	Query       string
}

type browseResultsData struct {
	SourceID  string
	Mangas    []models.Manga
	Page      int
	Query     string
	HasMore   bool
	InLibrary map[string]bool
	Error     string
}

type sourceMangaData struct {
	CurrentPath string
	SourceID    string
	SourceName  string
	Manga       models.Manga
	InLibrary   bool
}

func NewSources(svc *service.Service, templates map[string]*template.Template) *Sources {
	return &Sources{svc: svc, templates: templates}
}

func (s *Sources) Index(w http.ResponseWriter, r *http.Request) {
	infos := s.svc.Sources()
	if len(infos) > 0 {
		http.Redirect(w, r, "/sources/"+infos[0].ID, http.StatusFound)
		return
	}

	renderTemplate(w, s.templates, "sources.html", sourceBrowseData{
		CurrentPath: "sources",
		Sources:     infos,
	})
}

func (s *Sources) Browse(w http.ResponseWriter, r *http.Request) {
	sourceID := r.PathValue("id")
	info, ok := s.svc.SourceInfo(sourceID)
	if !ok {
		http.NotFound(w, r)
		return
	}

	renderTemplate(w, s.templates, "sources.html", sourceBrowseData{
		CurrentPath: "sources",
		Sources:     s.svc.Sources(),
		SourceID:    sourceID,
		SourceName:  info.Name,
		Query:       strings.TrimSpace(r.URL.Query().Get("q")),
	})
}

func (s *Sources) BrowseResults(w http.ResponseWriter, r *http.Request) {
	userID := getUserID(r.Context())
	sourceID := r.PathValue("id")

	page, err := strconv.Atoi(r.URL.Query().Get("page"))
	if err != nil || page < 1 {
		page = 1
	}
	size := 25

	mangas, err := s.svc.SourceLatest(r.Context(), sourceID, page, size)
	if err != nil {
		if errors.Is(err, service.ErrUnknownSource) {
			http.NotFound(w, r)
			return
		}
		renderTemplateName(w, s.templates, "sources.html", "browse-results-partial", browseResultsData{
			SourceID: sourceID,
			Page:     page,
			Error:    err.Error(),
		})
		return
	}

	renderTemplateName(w, s.templates, "sources.html", "browse-results-partial", browseResultsData{
		SourceID:  sourceID,
		Mangas:    mangas,
		Page:      page,
		HasMore:   len(mangas) > 0,
		InLibrary: s.librarySlugMap(r, userID, sourceID),
	})
}

func (s *Sources) Search(w http.ResponseWriter, r *http.Request) {
	userID := getUserID(r.Context())
	sourceID := r.PathValue("id")

	page, err := strconv.Atoi(r.URL.Query().Get("page"))
	if err != nil || page < 1 {
		page = 1
	}
	size := 25
	query := strings.TrimSpace(r.URL.Query().Get("q"))

	var mangas []models.Manga
	if query == "" {
		mangas, err = s.svc.SourceLatest(r.Context(), sourceID, page, size)
	} else {
		mangas, err = s.svc.SourceSearch(r.Context(), sourceID, query, page, size)
	}

	if err != nil {
		if errors.Is(err, service.ErrUnknownSource) {
			http.NotFound(w, r)
			return
		}
		renderTemplateName(w, s.templates, "sources.html", "browse-results-partial", browseResultsData{
			SourceID: sourceID,
			Page:     page,
			Query:    query,
			Error:    err.Error(),
		})
		return
	}

	// TeamX keyword search returns all matches on page 1; don't endlessly paginate empty queries.
	hasMore := len(mangas) >= size
	if query == "" {
		hasMore = len(mangas) > 0
	}

	renderTemplateName(w, s.templates, "sources.html", "browse-results-partial", browseResultsData{
		SourceID:  sourceID,
		Mangas:    mangas,
		Page:      page,
		Query:     query,
		HasMore:   hasMore,
		InLibrary: s.librarySlugMap(r, userID, sourceID),
	})
}

func (s *Sources) MangaDetail(w http.ResponseWriter, r *http.Request) {
	userID := getUserID(r.Context())
	sourceID := r.PathValue("id")
	slug := r.PathValue("slug")

	info, ok := s.svc.SourceInfo(sourceID)
	if !ok {
		http.NotFound(w, r)
		return
	}

	manga, err := s.svc.SourceManga(r.Context(), sourceID, slug)
	if err != nil {
		if errors.Is(err, service.ErrUnknownSource) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if manga == nil {
		http.NotFound(w, r)
		return
	}

	inLibrary := s.librarySlugMap(r, userID, sourceID)[slug]

	renderTemplate(w, s.templates, "source-manga.html", sourceMangaData{
		CurrentPath: "sources",
		SourceID:    sourceID,
		SourceName:  info.Name,
		Manga:       *manga,
		InLibrary:   inLibrary,
	})
}

func (s *Sources) Import(w http.ResponseWriter, r *http.Request) {
	sourceID := r.PathValue("id")
	slug := r.PathValue("slug")
	userID := getUserID(r.Context())

	if err := s.svc.Favorite(r.Context(), userID, sourceID, slug); err != nil {
		writeActionError(w, err)
		return
	}

	writeToast(w, "Added to library", "success")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `
		<div class="btn-group">
			<span class="pill pill-success">In Library</span>
			<a href="/manga/%s/%s" class="btn btn-primary">Open in Library</a>
		</div>
	`, sourceID, slug)
}

func (s *Sources) librarySlugMap(r *http.Request, userID int, sourceID string) map[string]bool {
	out := make(map[string]bool)
	library, err := s.svc.Library(r.Context(), userID)
	if err != nil {
		return out
	}
	for _, entry := range library {
		if entry.SourceID == sourceID {
			out[entry.Slug] = true
		}
	}
	return out
}
