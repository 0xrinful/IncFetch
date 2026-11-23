package server

import (
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/0xrinful/InkFetch/internal/models"
	"github.com/0xrinful/InkFetch/internal/queue"
	"github.com/0xrinful/InkFetch/internal/service"
)

var (
	muxMu         sync.Mutex
	registeredMux http.Handler
	sessionSecret string
)

type ActionSection struct {
	Label      string
	Endpoint   string
	HasRange   bool
	IsDelete   bool
	IsDownload bool
}

type ChapterRowData struct {
	SourceID  string
	MangaSlug string
	Chapter   models.ChapterRecord
	IsRead    bool
}

type jobFilter struct {
	Status string
	Label  string
	Count  int
	Active bool
}

type Server struct {
	service   *service.Service
	templates map[string]*template.Template
}

func SetSessionSecret(secret string) {
	muxMu.Lock()
	sessionSecret = secret
	registeredMux = nil
	muxMu.Unlock()
}

func actionSections(sourceID, mangaSlug string) []ActionSection {
	base := fmt.Sprintf("/manga/%s/%s", sourceID, mangaSlug)
	return []ActionSection{
		{Label: "Download range", Endpoint: base + "/download", HasRange: true},
		{Label: "Optimize range", Endpoint: base + "/optimize", HasRange: true},
		{Label: "Pack to CBZ", Endpoint: base + "/pack", HasRange: true},
		{
			Label:      "Export ZIP archive",
			Endpoint:   base + "/download/zip",
			HasRange:   true,
			IsDownload: true,
		},
		{Label: "Delete files", Endpoint: base + "/files", IsDelete: true},
	}
}

func chapterRowData(
	sourceID string,
	mangaSlug string,
	chapter models.ChapterRecord,
	readMarks map[float64]bool,
) ChapterRowData {
	return ChapterRowData{
		SourceID:  sourceID,
		MangaSlug: mangaSlug,
		Chapter:   chapter,
		IsRead:    readMarks[chapter.Number],
	}
}

func jobStatusSlug(status queue.JobStatus) string {
	switch status {
	case queue.JobRunning:
		return "running"
	case queue.JobDone:
		return "done"
	case queue.JobFailed:
		return "failed"
	case queue.JobCancelled:
		return "cancelled"
	default:
		return "pending"
	}
}

func jobStatusLabel(status queue.JobStatus) string {
	switch status {
	case queue.JobRunning:
		return "Running"
	case queue.JobDone:
		return "Done"
	case queue.JobFailed:
		return "Failed"
	case queue.JobCancelled:
		return "Cancelled"
	default:
		return "Queued"
	}
}

func jobTypeLabel(kind queue.JobType) string {
	switch kind {
	case queue.JobDownload:
		return "Download"
	case queue.JobOptimize:
		return "Optimize"
	case queue.JobPack:
		return "Pack"
	default:
		return "Task"
	}
}

func formatChapterNum(n float64) string {
	return strconv.FormatFloat(n, 'f', -1, 64)
}

func shouldShowTitle(ch models.ChapterRecord) bool {
	title := strings.TrimSpace(ch.Title)
	if title == "" {
		return false
	}
	numStr := formatChapterNum(ch.Number)
	if title == numStr || title == "Chapter "+numStr || title == "الفصل "+numStr || title == "الفصل رقم "+numStr {
		return false
	}
	return true
}

func pctInt(done, total int) int {
	if total <= 0 || done <= 0 {
		return 0
	}
	if done >= total {
		return 100
	}
	return (done * 100) / total
}

func formatSpeed(bps float64) string {
	if bps <= 0 {
		return "—"
	}
	const (
		kb = 1024.0
		mb = 1024.0 * 1024.0
	)
	switch {
	case bps >= mb:
		return fmt.Sprintf("%.1f MB/s", bps/mb)
	case bps >= kb:
		return fmt.Sprintf("%.0f KB/s", bps/kb)
	default:
		return fmt.Sprintf("%.0f B/s", bps)
	}
}

func formatBytesShort(n int64) string {
	if n <= 0 {
		return "0 B"
	}
	units := []string{"B", "KB", "MB", "GB", "TB"}
	val := float64(n)
	u := 0
	for val >= 1024 && u < len(units)-1 {
		val /= 1024
		u++
	}
	if u == 0 {
		return fmt.Sprintf("%d B", n)
	}
	return fmt.Sprintf("%.1f %s", val, units[u])
}

func New(svc *service.Service) *Server {
	funcMap := template.FuncMap{
		"actionSections":   actionSections,
		"chapterRowData":   chapterRowData,
		"shouldShowTitle":  shouldShowTitle,
		"formatChapterNum": formatChapterNum,
		"jobStatusSlug":    jobStatusSlug,
		"jobStatusLabel":   jobStatusLabel,
		"jobTypeLabel":     jobTypeLabel,
		"pctInt":           pctInt,
		"formatSpeed":      formatSpeed,
		"formatBytes":      formatBytesShort,
		"urlquery":         url.QueryEscape,
		"lower":            strings.ToLower,
		"sourceLabel": func(id string) string {
			switch id {
			case "mangalek":
				return "MangaLek"
			case "azora":
				return "Azora"
			case "teamx":
				return "Team X"
			default:
				return id
			}
		},
		"sub":              func(a, b int) int { return a - b },
		"add":              func(a, b int) int { return a + b },
		"countDownloaded": func(chapters []models.ChapterRecord) int {
			n := 0
			for _, c := range chapters {
				if c.Downloaded {
					n++
				}
			}
			return n
		},
		"countPacked": func(chapters []models.ChapterRecord) int {
			n := 0
			for _, c := range chapters {
				if c.Packed {
					n++
				}
			}
			return n
		},
	}

	templatesCache := make(map[string]*template.Template)

	patterns := []string{
		filepath.Join("ui", "templates", "partials", "*.html"),
		filepath.Join("ui", "templates", "components", "*.html"),
		filepath.Join("ui", "templates", "layouts", "*.html"),
	}

	sharedFiles := []string{}
	for _, pattern := range patterns {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			slog.Error("server: global glob error", "pattern", pattern, "err", err)
			continue
		}
		sharedFiles = append(sharedFiles, matches...)
	}

	pages, err := filepath.Glob(filepath.Join("ui", "templates", "pages", "*.html"))
	if err != nil {
		slog.Error("server: pages glob error", "err", err)
	}

	for _, page := range pages {
		name := filepath.Base(page)
		filesToParse := append([]string{page}, sharedFiles...)
		t, err := template.New(name).Funcs(funcMap).ParseFiles(filesToParse...)
		if err != nil {
			slog.Error("server: failed to parse template bundle", "page", name, "err", err)
			continue
		}
		templatesCache[name] = t
	}

	return &Server{
		service:   svc,
		templates: templatesCache,
	}
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	muxMu.Lock()
	if registeredMux == nil {
		registeredMux = routes(s, sessionSecret)
	}
	mux := registeredMux
	muxMu.Unlock()

	mux.ServeHTTP(w, r)
}
