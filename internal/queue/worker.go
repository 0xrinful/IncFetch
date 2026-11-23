package queue

import (
	"context"
	"fmt"
	"sync"

	"github.com/0xrinful/InkFetch/internal/models"
	"github.com/0xrinful/InkFetch/internal/optimizer"
	"github.com/0xrinful/InkFetch/internal/packer"
	"github.com/0xrinful/InkFetch/internal/registry"
	"github.com/0xrinful/InkFetch/internal/storage/db"
	"github.com/0xrinful/InkFetch/internal/storage/files"
)

type SettingsProvider func() models.AppSettings

type Config struct {
	AutoOptimize bool
	AutoPack     bool
}

type Worker struct {
	queue     *Queue
	registry  *registry.Registry
	optimizer *optimizer.Optimizer
	packer    *packer.Packer
	settings  SettingsProvider
	db        *db.DB
	files     *files.Store
	wg        sync.WaitGroup
}

func NewWorker(
	q *Queue,
	reg *registry.Registry,
	cfg Config,
	database *db.DB,
	fileStore *files.Store,
) *Worker {
	defaultProvider := func() models.AppSettings {
		s := models.DefaultSettings()
		s.AutoOptimize = cfg.AutoOptimize
		s.AutoPack = cfg.AutoPack
		return s
	}
	return NewWorkerWithSettings(q, reg, defaultProvider, database, fileStore)
}

func NewWorkerWithSettings(
	q *Queue,
	reg *registry.Registry,
	provider SettingsProvider,
	database *db.DB,
	fileStore *files.Store,
) *Worker {
	if provider == nil {
		provider = models.DefaultSettings
	}
	return &Worker{
		queue:     q,
		registry:  reg,
		optimizer: optimizer.New(),
		packer:    packer.New(),
		settings:  provider,
		db:        database,
		files:     fileStore,
	}
}

func (w *Worker) Start(ctx context.Context) {
	w.wg.Add(3)
	go func() {
		defer w.wg.Done()
		w.runPool(ctx, JobDownload, func() int {
			return w.settings().Normalized().DownloadConcurrency
		})
	}()
	go func() {
		defer w.wg.Done()
		w.runPool(ctx, JobOptimize, func() int { return 2 })
	}()
	go func() {
		defer w.wg.Done()
		w.runPool(ctx, JobPack, func() int { return 3 })
	}()
}

func (w *Worker) Wait() {
	w.wg.Wait()
}

func (w *Worker) runPool(ctx context.Context, kind JobType, maxLimit func() int) {
	var (
		mu      sync.Mutex
		cond    = sync.NewCond(&mu)
		active  int
		localWG sync.WaitGroup
	)

	go func() {
		<-ctx.Done()
		mu.Lock()
		cond.Broadcast()
		mu.Unlock()
	}()

	for {
		mu.Lock()
		for active >= maxLimit() && ctx.Err() == nil {
			cond.Wait()
		}
		if ctx.Err() != nil {
			mu.Unlock()
			break
		}
		mu.Unlock()

		job := w.queue.next(ctx, kind)
		if job == nil {
			break
		}

		mu.Lock()
		active++
		mu.Unlock()

		localWG.Add(1)
		go func(j *Job) {
			defer func() {
				mu.Lock()
				active--
				cond.Signal()
				mu.Unlock()
				localWG.Done()
			}()
			w.process(ctx, j)
		}(job)
	}

	localWG.Wait()
}

func (w *Worker) process(ctx context.Context, job *Job) {
	jobCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	if !w.queue.markRunning(job, cancel) {
		return
	}

	cfg := w.settings().Normalized()

	var err error
	switch job.Type {
	case JobDownload:
		src, ok := w.registry.Source(job.Chapter.SourceID)
		if !ok {
			err = fmt.Errorf("unknown source %q", job.Chapter.SourceID)
			break
		}

		pages, e := src.Pages(jobCtx, job.Chapter.URL)
		if e != nil {
			err = e
			break
		}
		if len(pages) == 0 {
			err = fmt.Errorf("source returned 0 pages for chapter")
			break
		}

		chapter := job.Chapter
		chapter.Pages = pages

		dl, ok := w.registry.Downloader(job.Chapter.SourceID)
		if !ok {
			err = fmt.Errorf("no downloader for source %q", job.Chapter.SourceID)
			break
		}

		err = dl.DownloadChapterWithProgress(
			jobCtx,
			chapter,
			job.DestDir,
			cfg.PageConcurrency,
			func(completed, total int, bytesDelta int64) {
				w.queue.updateProgress(job, completed, total, bytesDelta)
			},
		)
		if err == nil {
			err = w.db.MarkDownloaded(job.Chapter, job.DestDir)
		}
		if err == nil && cfg.AutoOptimize {
			w.queue.Enqueue(&Job{
				Type:    JobOptimize,
				Chapter: job.Chapter,
				SrcDir:  job.DestDir,
				DestDir: w.files.OptimizedDir(job.Chapter),
			})
		} else if err == nil && cfg.AutoPack {
			w.queue.Enqueue(&Job{
				Type:     JobPack,
				Chapter:  job.Chapter,
				SrcDir:   job.DestDir,
				DestFile: w.files.CBZPath(job.Chapter),
			})
		}

	case JobOptimize:
		if e := w.files.EnsureDir(job.DestDir); e != nil {
			err = e
			break
		}

		err = w.optimizer.OptimizeChapterWithOptions(
			jobCtx,
			job.SrcDir,
			job.DestDir,
			cfg.OptimizeQuality,
			func(completed, total int) {
				w.queue.updateProgress(job, completed, total, 0)
			},
		)
		if err == nil {
			err = w.db.MarkOptimized(job.Chapter, job.DestDir)
		}
		if err == nil && cfg.AutoPack {
			w.queue.Enqueue(&Job{
				Type:     JobPack,
				Chapter:  job.Chapter,
				SrcDir:   job.DestDir,
				DestFile: w.files.CBZPath(job.Chapter),
			})
		}

	case JobPack:
		err = w.packer.Pack(jobCtx, job.Chapter, job.SrcDir, job.DestFile)
		if err == nil {
			err = w.db.MarkPacked(job.Chapter, job.DestFile)
		}
	}

	w.queue.markFinished(job, err)
}
