package service

import (
	"errors"
	"log/slog"
	"sync"

	"github.com/0xrinful/InkFetch/internal/models"
	"github.com/0xrinful/InkFetch/internal/packer"
	"github.com/0xrinful/InkFetch/internal/queue"
	"github.com/0xrinful/InkFetch/internal/registry"
	"github.com/0xrinful/InkFetch/internal/storage/db"
	"github.com/0xrinful/InkFetch/internal/storage/files"
)

var (
	ErrInvalidCredentials = errors.New("service: invalid credentials")
	ErrUnknownSource      = errors.New("service: unknown source")
	ErrNotFound           = errors.New("service: not found")
	ErrNotDownloaded      = errors.New("service: chapter not downloaded")
)

type Service struct {
	registry   *registry.Registry
	db         *db.DB
	files      *files.Store
	queue      *queue.Queue
	packer     *packer.Packer
	settingsMu sync.RWMutex
	settings   models.AppSettings
}

func New(
	registry *registry.Registry,
	database *db.DB,
	fileStore *files.Store,
	q *queue.Queue,
) *Service {
	cfg := models.DefaultSettings()
	if database != nil {
		loaded, err := database.LoadSettings()
		if err != nil {
			slog.Warn("failed to load settings from db, using defaults", "err", err)
		} else {
			cfg = loaded
		}
	}

	return &Service{
		registry: registry,
		db:       database,
		files:    fileStore,
		queue:    q,
		packer:   packer.New(),
		settings: cfg,
	}
}

func (s *Service) Files() *files.Store {
	return s.files
}

func (s *Service) FlareEnabled() bool {
	return s.registry.FlareEnabled()
}

func (s *Service) Settings() models.AppSettings {
	s.settingsMu.RLock()
	defer s.settingsMu.RUnlock()
	return s.settings
}

func (s *Service) UpdateSettings(cfg models.AppSettings) error {
	cfg = cfg.Normalized()
	if err := s.db.SaveSettings(cfg); err != nil {
		return err
	}
	s.settingsMu.Lock()
	s.settings = cfg
	s.settingsMu.Unlock()
	return nil
}

func (s *Service) QueueStats() queue.QueueStats {
	return s.queue.Stats()
}

func (s *Service) CancelJob(id int) error {
	return s.queue.Cancel(id)
}

func (s *Service) RetryJob(id int) (int, error) {
	return s.queue.Retry(id)
}

func (s *Service) RetryAllFailed() int {
	return s.queue.RetryAllFailed()
}

func (s *Service) ClearFinishedJobs() int {
	return s.queue.ClearFinished()
}

func (s *Service) PauseQueue() {
	s.queue.Pause()
}

func (s *Service) ResumeQueue() {
	s.queue.Resume()
}

func (s *Service) IsQueuePaused() bool {
	return s.queue.IsPaused()
}
