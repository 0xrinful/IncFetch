package handlers

import (
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode"

	"github.com/0xrinful/InkFetch/internal/models"
	"github.com/0xrinful/InkFetch/internal/queue"
	"github.com/0xrinful/InkFetch/internal/service"
)

type Dashboard struct {
	svc       *service.Service
	templates map[string]*template.Template
}

type jobDesc struct {
	ID              int
	Type            queue.JobType
	Title           string
	MangaSlug       string
	SourceID        string
	ChapterNum      float64
	Status          queue.JobStatus
	CreatedAt       time.Time
	StartedAt       *time.Time
	DoneAt          *time.Time
	Error           string
	TotalItems      int
	CompletedItems  int
	BytesDownloaded int64
	SpeedBps        float64
	CanCancel       bool
	CanRetry        bool
}

type jobsListData struct {
	Jobs         []jobDesc
	Stats        queue.QueueStats
	ActiveStatus string
	Expanded     int
	OOB          bool
}

type downloadsPageData struct {
	CurrentPath string
	MangaCount  int
	Stats       queue.QueueStats
	InitialJobs jobsListData
	Storage     storagePartialData
}

type settingsPageData struct {
	CurrentPath    string
	Settings       models.AppSettings
	Storage        storagePartialData
	FilesRoot      string
	FlareEnabled   bool
	IsFlareRunning bool
	SourcesCount   int
}

type storagePartialData struct {
	UsedBytes  int64
	TotalBytes uint64
	Used       string
	Total      string
	Percent    float64
}

func NewDashboard(svc *service.Service, templates map[string]*template.Template) *Dashboard {
	return &Dashboard{svc: svc, templates: templates}
}

func (d *Dashboard) Page(w http.ResponseWriter, r *http.Request) {
	jobs := d.svc.Jobs()
	stats := d.svc.QueueStats()
	mangas, err := d.svc.Library(r.Context(), getUserID(r.Context()))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	storageData, _ := d.computeStorage()

	renderTemplate(w, d.templates, "downloads.html", downloadsPageData{
		CurrentPath: "downloads",
		MangaCount:  len(mangas),
		Stats:       stats,
		Storage:     storageData,
		InitialJobs: jobsListData{
			Jobs:         describeJobs(jobs),
			Stats:        stats,
			ActiveStatus: "all",
			Expanded:     0,
		},
	})
}

func (d *Dashboard) Jobs(w http.ResponseWriter, r *http.Request) {
	status := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("status")))
	if status == "" {
		status = "all"
	}
	expanded, _ := strconv.Atoi(r.URL.Query().Get("expanded"))

	d.renderJobsBundle(w, status, expanded)
}

func (d *Dashboard) renderJobsBundle(w http.ResponseWriter, status string, expanded int) {
	jobs := d.svc.Jobs()
	stats := d.svc.QueueStats()
	filtered := filterJobsByStatus(jobs, status)

	renderTemplateName(w, d.templates, "downloads.html", "jobs-bundle-partial", jobsListData{
		Jobs:         describeJobs(filtered),
		Stats:        stats,
		ActiveStatus: status,
		Expanded:     expanded,
		OOB:          true,
	})
}

func (d *Dashboard) JobDetail(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		http.Error(w, "invalid job id", http.StatusBadRequest)
		return
	}

	job, ok := d.svc.Job(id)
	if !ok {
		http.NotFound(w, r)
		return
	}

	renderTemplateName(w, d.templates, "downloads.html", "job-detail-partial", describeJob(job))
}

func (d *Dashboard) Badge(w http.ResponseWriter, r *http.Request) {
	stats := d.svc.QueueStats()
	active := stats.Running + stats.Pending
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if active > 0 {
		fmt.Fprintf(w, `<span class="nav-badge nav-badge-active">%d</span>`, active)
		return
	}
	if stats.Failed > 0 {
		fmt.Fprintf(w, `<span class="nav-badge nav-badge-error">%d</span>`, stats.Failed)
	}
}

func (d *Dashboard) CancelJob(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		writeActionError(w, errors.New("invalid job id"))
		return
	}
	if err := d.svc.CancelJob(id); err != nil {
		writeActionError(w, err)
		return
	}
	writeToast(w, fmt.Sprintf("Cancelled job #%d", id), "info")
	d.renderJobsBundle(w, r.URL.Query().Get("status"), 0)
}

func (d *Dashboard) RetryJob(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		writeActionError(w, errors.New("invalid job id"))
		return
	}
	newID, err := d.svc.RetryJob(id)
	if err != nil {
		writeActionError(w, err)
		return
	}
	writeToast(w, fmt.Sprintf("Retrying as job #%d", newID), "success")
	d.renderJobsBundle(w, r.URL.Query().Get("status"), 0)
}

func (d *Dashboard) RetryAllFailed(w http.ResponseWriter, r *http.Request) {
	n := d.svc.RetryAllFailed()
	if n == 0 {
		writeToast(w, "No failed jobs to retry", "info")
	} else {
		writeToast(w, fmt.Sprintf("Retrying %d failed job(s)", n), "success")
	}
	d.renderJobsBundle(w, r.URL.Query().Get("status"), 0)
}

func (d *Dashboard) ClearFinished(w http.ResponseWriter, r *http.Request) {
	n := d.svc.ClearFinishedJobs()
	if n == 0 {
		writeToast(w, "No finished jobs to clear", "info")
	} else {
		writeToast(w, fmt.Sprintf("Cleared %d finished job(s)", n), "success")
	}
	d.renderJobsBundle(w, r.URL.Query().Get("status"), 0)
}

func (d *Dashboard) PauseQueue(w http.ResponseWriter, r *http.Request) {
	d.svc.PauseQueue()
	writeToast(w, "Download queue paused", "info")
	d.renderJobsBundle(w, r.URL.Query().Get("status"), 0)
}

func (d *Dashboard) ResumeQueue(w http.ResponseWriter, r *http.Request) {
	d.svc.ResumeQueue()
	writeToast(w, "Download queue resumed", "success")
	d.renderJobsBundle(w, r.URL.Query().Get("status"), 0)
}

func (d *Dashboard) Storage(w http.ResponseWriter, r *http.Request) {
	data, err := d.computeStorage()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	renderTemplateName(w, d.templates, "downloads.html", "storage-partial", data)
}

func (d *Dashboard) SettingsPage(w http.ResponseWriter, r *http.Request) {
	storageData, _ := d.computeStorage()
	flareEnabled := d.svc.FlareEnabled()
	isRunning := false
	if flareEnabled {
		isRunning = IsFlareRunning()
	}

	renderTemplate(w, d.templates, "settings.html", settingsPageData{
		CurrentPath:    "settings",
		Settings:       d.svc.Settings(),
		Storage:        storageData,
		FilesRoot:      d.svc.Files().Root(),
		FlareEnabled:   flareEnabled,
		IsFlareRunning: isRunning,
		SourcesCount:   len(d.svc.Sources()),
	})
}

func (d *Dashboard) SaveSettings(w http.ResponseWriter, r *http.Request) {
	noSwap(w)

	if err := r.ParseForm(); err != nil {
		writeActionError(w, err)
		return
	}

	cfg := d.svc.Settings()
	if v, err := strconv.Atoi(strings.TrimSpace(r.FormValue("download_concurrency"))); err == nil {
		cfg.DownloadConcurrency = v
	}
	if v, err := strconv.Atoi(strings.TrimSpace(r.FormValue("page_concurrency"))); err == nil {
		cfg.PageConcurrency = v
	}
	if v, err := strconv.Atoi(strings.TrimSpace(r.FormValue("optimize_quality"))); err == nil {
		cfg.OptimizeQuality = v
	}
	cfg.AutoOptimize = r.FormValue("auto_optimize") == "on"
	cfg.AutoPack = r.FormValue("auto_pack") == "on"

	if err := d.svc.UpdateSettings(cfg); err != nil {
		writeActionError(w, err)
		return
	}

	writeToast(w, "Settings saved", "success")
	w.WriteHeader(http.StatusOK)
}

func (d *Dashboard) computeStorage() (storagePartialData, error) {
	root := d.svc.Files().Root()
	used, err := usedBytes(root)
	if err != nil {
		return storagePartialData{}, err
	}

	var total uint64
	var stat syscall.Statfs_t
	if err := syscall.Statfs(root, &stat); err == nil {
		total = stat.Blocks * uint64(stat.Bsize)
	}

	data := storagePartialData{
		UsedBytes:  used,
		TotalBytes: total,
		Used:       formatBytes(uint64(used)),
		Total:      formatOptionalBytes(total),
	}
	if total > 0 {
		data.Percent = (float64(used) / float64(total)) * 100
	}
	return data, nil
}

func IsFlareRunning() bool {
	cmd := exec.Command(
		"docker", "inspect", "-f",
		"{{.State.Running}}",
		"flaresolverr",
	)
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) == "true"
}

func (d *Dashboard) StartFlareSolver(w http.ResponseWriter, r *http.Request) {
	if !d.svc.FlareEnabled() {
		writeToast(w, "FlareSolver is disabled in .env", "error")
		w.WriteHeader(http.StatusUnprocessableEntity)
		return
	}

	cmd := exec.Command("docker", "start", "flaresolverr")
	if output, err := cmd.CombinedOutput(); err != nil {
		message := strings.TrimSpace(string(output))
		if message == "" {
			message = err.Error()
		}
		writeToast(w, message, "error")
		w.WriteHeader(http.StatusUnprocessableEntity)
		return
	}

	writeToast(w, "FlareSolver started", "success")
	renderTemplateName(w, d.templates, "settings.html", "flare-status-partial-running", nil)
}

func (d *Dashboard) StopFlareSolver(w http.ResponseWriter, r *http.Request) {
	if !d.svc.FlareEnabled() {
		writeToast(w, "FlareSolver is disabled in .env", "error")
		w.WriteHeader(http.StatusUnprocessableEntity)
		return
	}

	cmd := exec.Command("docker", "stop", "flaresolverr")
	if output, err := cmd.CombinedOutput(); err != nil {
		message := strings.TrimSpace(string(output))
		if message == "" {
			message = err.Error()
		}
		writeToast(w, message, "error")
		w.WriteHeader(http.StatusUnprocessableEntity)
		return
	}

	writeToast(w, "FlareSolver stopped", "success")
	renderTemplateName(w, d.templates, "settings.html", "flare-status-partial-stopped", nil)
}

func describeJobs(jobs []*queue.Job) []jobDesc {
	desc := make([]jobDesc, 0, len(jobs))
	for i := len(jobs) - 1; i >= 0; i-- {
		desc = append(desc, describeJob(jobs[i]))
	}
	return desc
}

func describeJob(job *queue.Job) jobDesc {
	slug := titleSlug(job.Chapter.MangaSlug)
	num := strconv.FormatFloat(job.Chapter.Number, 'f', -1, 64)
	desc := jobDesc{
		ID:              job.ID,
		Type:            job.Type,
		Title:           fmt.Sprintf("%s · Ch. %s", slug, num),
		MangaSlug:       job.Chapter.MangaSlug,
		SourceID:        job.Chapter.SourceID,
		ChapterNum:      job.Chapter.Number,
		Status:          job.Status,
		CreatedAt:       job.CreatedAt,
		Error:           job.Error,
		TotalItems:      job.TotalItems,
		CompletedItems:  job.CompletedItems,
		BytesDownloaded: job.BytesDownloaded,
		SpeedBps:        job.SpeedBps,
		CanCancel:       job.Status == queue.JobPending || job.Status == queue.JobRunning,
		CanRetry:        job.Status == queue.JobFailed || job.Status == queue.JobCancelled,
	}
	if !job.StartedAt.IsZero() {
		t := job.StartedAt
		desc.StartedAt = &t
	}
	if !job.DoneAt.IsZero() {
		t := job.DoneAt
		desc.DoneAt = &t
	}
	return desc
}

func filterJobsByStatus(jobs []*queue.Job, rawStatus string) []*queue.Job {
	status, ok := parseJobStatus(rawStatus)
	if !ok {
		return jobs
	}

	filtered := make([]*queue.Job, 0, len(jobs))
	for _, job := range jobs {
		if job.Status == status {
			filtered = append(filtered, job)
		}
	}
	return filtered
}

func parseJobStatus(raw string) (queue.JobStatus, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "pending", "queued":
		return queue.JobPending, true
	case "running", "active":
		return queue.JobRunning, true
	case "done", "completed":
		return queue.JobDone, true
	case "failed":
		return queue.JobFailed, true
	case "cancelled":
		return queue.JobCancelled, true
	default:
		return queue.JobPending, false
	}
}

func titleSlug(slug string) string {
	words := strings.Fields(strings.ReplaceAll(slug, "-", " "))
	for i, word := range words {
		words[i] = titleWord(word)
	}
	return strings.Join(words, " ")
}

func titleWord(word string) string {
	if word == "" {
		return word
	}
	runes := []rune(strings.ToLower(word))
	runes[0] = unicode.ToTitle(runes[0])
	return string(runes)
}

func usedBytes(root string) (int64, error) {
	var total int64
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return err
		}
		if info == nil || info.IsDir() {
			return nil
		}
		total += info.Size()
		return nil
	})
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	return total, err
}

func formatBytes(bytes uint64) string {
	if bytes == 0 {
		return "0 B"
	}
	units := []string{"B", "KB", "MB", "GB", "TB", "PB"}
	value := float64(bytes)
	unit := 0
	for value >= 1024 && unit < len(units)-1 {
		value /= 1024
		unit++
	}
	if unit == 0 {
		return fmt.Sprintf("%d %s", bytes, units[unit])
	}
	return fmt.Sprintf("%.1f %s", value, units[unit])
}

func formatOptionalBytes(bytes uint64) string {
	if bytes == 0 {
		return "—"
	}
	return formatBytes(bytes)
}
