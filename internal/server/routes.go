package server

import (
	"net/http"

	"github.com/0xrinful/InkFetch/internal/handlers"
)

func routes(s *Server, secret string) *http.ServeMux {
	mux := http.NewServeMux()

	fs := http.FileServer(http.Dir("ui/static"))
	mux.Handle("/static/", http.StripPrefix("/static/", fs))

	auth := handlers.NewAuth(
		s.service,
		s.templates,
		func(w http.ResponseWriter, r *http.Request, userID int64) {
			SetSession(w, r, &Session{UserID: userID}, secret)
		},
		func(w http.ResponseWriter) {
			ClearSession(w)
		},
	)

	library := handlers.NewLibrary(s.service, s.templates)
	manga := handlers.NewManga(s.service, s.templates)
	chapter := handlers.NewChapter(s.service, s.templates)
	viewer := handlers.NewViewer(s.service, s.templates)
	sources := handlers.NewSources(s.service, s.templates)
	dashboard := handlers.NewDashboard(s.service, s.templates)

	mux.Handle("GET /login", http.HandlerFunc(auth.LoginPage))
	mux.Handle("POST /login", http.HandlerFunc(auth.LoginSubmit))
	mux.Handle("GET /signup", http.HandlerFunc(auth.SignupPage))
	mux.Handle("POST /signup", http.HandlerFunc(auth.SignupSubmit))
	mux.Handle("POST /logout", http.HandlerFunc(auth.Logout))

	requireAuth := func(h http.HandlerFunc) http.Handler {
		return AuthRequired(secret, h)
	}

	requireAdmin := func(h http.HandlerFunc) http.Handler {
		return AdminRequired(secret, h)
	}

	// Library
	mux.Handle("GET /{$}", requireAuth(library.Index))

	// Manga detail & bulk/range actions
	mux.Handle("GET /manga/{sourceID}/{slug}", requireAuth(manga.Detail))
	mux.Handle("GET /manga/{sourceID}/{slug}/cover", requireAuth(manga.Cover))
	mux.Handle("POST /manga/{sourceID}/{slug}/download", requireAuth(manga.Download))
	mux.Handle("POST /manga/{sourceID}/{slug}/optimize", requireAuth(manga.Optimize))
	mux.Handle("POST /manga/{sourceID}/{slug}/pack", requireAuth(manga.Pack))
	mux.Handle("POST /manga/{sourceID}/{slug}/refresh", requireAuth(manga.Refresh))
	mux.Handle("POST /manga/{sourceID}/{slug}/sync", requireAuth(manga.Refresh))
	mux.Handle("POST /manga/{sourceID}/{slug}/chapters/bulk", requireAuth(manga.BulkAction))
	mux.Handle("POST /manga/{sourceID}/{slug}/download/zip", requireAuth(manga.DownloadPacked))
	mux.Handle("POST /manga/{sourceID}/{slug}/files", requireAuth(manga.DeleteFiles))
	mux.Handle("DELETE /manga/{sourceID}/{slug}/unfavorite", requireAuth(manga.Unfavorite))

	// Per-chapter actions
	mux.Handle("POST /manga/{sourceID}/{slug}/ch/{num}/download", requireAuth(chapter.Download))
	mux.Handle("POST /manga/{sourceID}/{slug}/ch/{num}/optimize", requireAuth(chapter.Optimize))
	mux.Handle("POST /manga/{sourceID}/{slug}/ch/{num}/pack", requireAuth(chapter.Pack))
	mux.Handle("POST /manga/{sourceID}/{slug}/ch/{num}/read", requireAuth(chapter.ToggleRead))

	// Reader
	mux.Handle("GET /manga/{sourceID}/{slug}/ch/{num}", requireAuth(viewer.Page))
	mux.Handle("GET /manga/{sourceID}/{slug}/ch/{num}/pages", requireAuth(viewer.Pages))
	mux.Handle("GET /manga/{sourceID}/{slug}/ch/{num}/img/{file}", requireAuth(viewer.Image))

	// Discover / Sources
	mux.Handle("GET /sources", requireAuth(sources.Index))
	mux.Handle("GET /sources/{id}", requireAuth(sources.Browse))
	mux.Handle("GET /sources/{id}/browse-results", requireAuth(sources.BrowseResults))
	mux.Handle("GET /sources/{id}/search", requireAuth(sources.Search))
	mux.Handle("GET /sources/{id}/manga/{slug}", requireAuth(sources.MangaDetail))
	mux.Handle("POST /sources/{id}/manga/{slug}/import", requireAuth(sources.Import))

	// Downloads & Queue Management (keep /dashboard alias for compatibility)
	mux.Handle("GET /downloads", requireAuth(dashboard.Page))
	mux.Handle("GET /dashboard", requireAuth(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/downloads", http.StatusFound)
	}))
	mux.Handle("GET /api/jobs", requireAuth(dashboard.Jobs))
	mux.Handle("GET /api/jobs/{id}", requireAuth(dashboard.JobDetail))
	mux.Handle("POST /api/jobs/{id}/cancel", requireAuth(dashboard.CancelJob))
	mux.Handle("POST /api/jobs/{id}/retry", requireAuth(dashboard.RetryJob))
	mux.Handle("POST /api/jobs/retry-failed", requireAuth(dashboard.RetryAllFailed))
	mux.Handle("POST /api/jobs/clear", requireAuth(dashboard.ClearFinished))
	mux.Handle("POST /api/jobs/pause", requireAuth(dashboard.PauseQueue))
	mux.Handle("POST /api/jobs/resume", requireAuth(dashboard.ResumeQueue))
	mux.Handle("GET /api/downloads/badge", requireAuth(dashboard.Badge))
	mux.Handle("GET /api/storage", requireAuth(dashboard.Storage))

	// Settings & FlareSolver controls
	mux.Handle("GET /settings", requireAuth(dashboard.SettingsPage))
	mux.Handle("POST /settings", requireAuth(dashboard.SaveSettings))
	mux.Handle("POST /api/flaresolver/start", requireAdmin(dashboard.StartFlareSolver))
	mux.Handle("POST /api/flaresolver/stop", requireAdmin(dashboard.StopFlareSolver))

	return mux
}
