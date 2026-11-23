package handlers

import (
	"html/template"
	"net/http"
	"sort"
	"strings"

	"github.com/0xrinful/InkFetch/internal/models"
	"github.com/0xrinful/InkFetch/internal/service"
	"github.com/0xrinful/InkFetch/internal/sources"
)

type Library struct {
	svc       *service.Service
	templates map[string]*template.Template
}

type libraryData struct {
	CurrentPath     string
	Mangas          []models.MangaSummary
	TotalCount      int
	FilteredCount   int
	DownloadedTotal int
	Sources         []sources.SourceInfo
	SourceNames     map[string]string
	Query           string
	Status          string
	SourceFilter    string
	DownloadFilter  string
	Sort            string
}

func NewLibrary(svc *service.Service, templates map[string]*template.Template) *Library {
	return &Library{svc: svc, templates: templates}
}

func (l *Library) Index(w http.ResponseWriter, r *http.Request) {
	userID := getUserID(r.Context())
	allMangas, err := l.svc.LibrarySummaries(r.Context(), userID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	query := strings.TrimSpace(r.URL.Query().Get("q"))
	status := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("status")))
	sourceFilter := strings.TrimSpace(r.URL.Query().Get("source"))
	downloadFilter := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("filter")))
	sortBy := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("sort")))
	if sortBy == "" {
		sortBy = "updated"
	}

	downloadedTotal := 0
	for _, m := range allMangas {
		downloadedTotal += m.DownloadedChapters
	}

	filtered := filterAndSortLibrary(allMangas, query, status, sourceFilter, downloadFilter, sortBy)

	allSources := l.svc.Sources()
	sourceNames := make(map[string]string, len(allSources))
	for _, src := range allSources {
		sourceNames[src.ID] = src.Name
	}

	data := libraryData{
		CurrentPath:     "library",
		Mangas:          filtered,
		TotalCount:      len(allMangas),
		FilteredCount:   len(filtered),
		DownloadedTotal: downloadedTotal,
		Sources:         allSources,
		SourceNames:     sourceNames,
		Query:           query,
		Status:          status,
		SourceFilter:    sourceFilter,
		DownloadFilter:  downloadFilter,
		Sort:            sortBy,
	}

	if r.Header.Get("HX-Request") == "true" {
		renderTemplateName(w, l.templates, "library.html", "library-grid-partial", data)
		return
	}

	renderTemplate(w, l.templates, "library.html", data)
}

func filterAndSortLibrary(
	mangas []models.MangaSummary,
	query, status, sourceFilter, downloadFilter, sortBy string,
) []models.MangaSummary {
	qLower := strings.ToLower(query)
	out := make([]models.MangaSummary, 0, len(mangas))

	for _, m := range mangas {
		if qLower != "" {
			matched := strings.Contains(strings.ToLower(m.Title), qLower) ||
				strings.Contains(strings.ToLower(m.Slug), qLower)
			if !matched {
				for _, g := range m.Genres {
					if strings.Contains(strings.ToLower(g), qLower) {
						matched = true
						break
					}
				}
			}
			if !matched {
				continue
			}
		}

		if status != "" && status != "all" && strings.ToLower(strings.TrimSpace(m.Status)) != status {
			continue
		}

		if sourceFilter != "" && sourceFilter != "all" && m.SourceID != sourceFilter {
			continue
		}

		switch downloadFilter {
		case "downloaded":
			if m.DownloadedChapters == 0 {
				continue
			}
		case "incomplete":
			if m.TotalChapters == 0 || m.DownloadedChapters >= m.TotalChapters {
				continue
			}
		case "unread":
			if m.TotalChapters == 0 || m.ReadChapters >= m.TotalChapters {
				continue
			}
		}

		out = append(out, m)
	}

	sort.SliceStable(out, func(i, j int) bool {
		switch sortBy {
		case "title":
			return strings.ToLower(out[i].Title) < strings.ToLower(out[j].Title)
		case "added":
			return out[i].AddedAt.After(out[j].AddedAt)
		case "chapters":
			if out[i].TotalChapters != out[j].TotalChapters {
				return out[i].TotalChapters > out[j].TotalChapters
			}
			return strings.ToLower(out[i].Title) < strings.ToLower(out[j].Title)
		case "downloaded":
			if out[i].DownloadedChapters != out[j].DownloadedChapters {
				return out[i].DownloadedChapters > out[j].DownloadedChapters
			}
			return strings.ToLower(out[i].Title) < strings.ToLower(out[j].Title)
		case "unread":
			ui := max(0, out[i].TotalChapters-out[i].ReadChapters)
			uj := max(0, out[j].TotalChapters-out[j].ReadChapters)
			if ui != uj {
				return ui > uj
			}
			return strings.ToLower(out[i].Title) < strings.ToLower(out[j].Title)
		default: // updated
			if !out[i].UpdatedAt.Equal(out[j].UpdatedAt) {
				return out[i].UpdatedAt.After(out[j].UpdatedAt)
			}
			return strings.ToLower(out[i].Title) < strings.ToLower(out[j].Title)
		}
	})

	return out
}
