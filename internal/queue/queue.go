package queue

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/0xrinful/InkFetch/internal/models"
)

type JobType int

const (
	JobDownload JobType = iota
	JobOptimize
	JobPack
)

type JobStatus int

const (
	JobPending JobStatus = iota
	JobRunning
	JobDone
	JobFailed
	JobCancelled
)

var (
	ErrJobNotFound     = errors.New("queue: job not found")
	ErrJobNotRetryable = errors.New("queue: only failed or cancelled jobs can be retried")
	ErrJobNotCancel    = errors.New("queue: only pending or running jobs can be cancelled")
)

type Job struct {
	ID              int
	Type            JobType
	Status          JobStatus
	CreatedAt       time.Time
	StartedAt       time.Time
	DoneAt          time.Time
	Error           string
	TotalItems      int
	CompletedItems  int
	BytesDownloaded int64
	SpeedBps        float64

	Chapter  models.Chapter
	SrcDir   string
	DestDir  string
	DestFile string

	cancel context.CancelFunc
}

type QueueStats struct {
	Total       int
	Pending     int
	Running     int
	Done        int
	Failed      int
	Cancelled   int
	Paused      bool
	TotalSpeed  float64
	ActiveBytes int64
}

type jobKey struct {
	jobType   JobType
	sourceID  string
	mangaSlug string
	chapter   float64
}

type Queue struct {
	mu       sync.RWMutex
	cond     *sync.Cond
	all      []*Job
	pending  map[JobType][]*Job
	byID     map[int]*Job
	nextID   int
	paused   bool
	closed   bool
}

func NewQueue() *Queue {
	q := &Queue{
		pending: map[JobType][]*Job{
			JobDownload: nil,
			JobOptimize: nil,
			JobPack:     nil,
		},
		byID: make(map[int]*Job),
	}
	q.cond = sync.NewCond(&q.mu)
	return q
}

func keyOf(job *Job) jobKey {
	return jobKey{
		jobType:   job.Type,
		sourceID:  job.Chapter.SourceID,
		mangaSlug: job.Chapter.MangaSlug,
		chapter:   job.Chapter.Number,
	}
}

// Enqueue schedules a job unless an identical pending/running job already exists.
// Returns the job ID and whether a new job was created.
func (q *Queue) Enqueue(job *Job) int {
	id, _ := q.EnqueueUnique(job)
	return id
}

func (q *Queue) EnqueueUnique(job *Job) (int, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()

	k := keyOf(job)
	for _, existing := range q.all {
		if (existing.Status == JobPending || existing.Status == JobRunning) && keyOf(existing) == k {
			return existing.ID, false
		}
	}

	q.nextID++
	cloned := *job
	cloned.ID = q.nextID
	cloned.CreatedAt = time.Now().UTC()
	cloned.Status = JobPending
	cloned.StartedAt = time.Time{}
	cloned.DoneAt = time.Time{}
	cloned.Error = ""
	cloned.TotalItems = 0
	cloned.CompletedItems = 0
	cloned.BytesDownloaded = 0
	cloned.SpeedBps = 0
	cloned.cancel = nil

	q.all = append(q.all, &cloned)
	q.byID[cloned.ID] = &cloned
	q.pending[cloned.Type] = append(q.pending[cloned.Type], &cloned)
	q.cond.Broadcast()

	return cloned.ID, true
}

// next blocks until a pending job of kind is available (and queue is not paused)
// or ctx is cancelled.
func (q *Queue) next(ctx context.Context, kind JobType) *Job {
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			q.mu.Lock()
			q.cond.Broadcast()
			q.mu.Unlock()
		case <-done:
		}
	}()
	defer close(done)

	q.mu.Lock()
	defer q.mu.Unlock()

	for {
		if ctx.Err() != nil || q.closed {
			return nil
		}
		if q.paused || len(q.pending[kind]) == 0 {
			q.cond.Wait()
			continue
		}
		job := q.pending[kind][0]
		q.pending[kind] = q.pending[kind][1:]
		if job.Status != JobPending {
			continue
		}
		return job
	}
}

func (q *Queue) markRunning(job *Job, cancel context.CancelFunc) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if job.Status != JobPending {
		return false
	}
	job.Status = JobRunning
	job.StartedAt = time.Now().UTC()
	job.DoneAt = time.Time{}
	job.Error = ""
	job.cancel = cancel
	return true
}

func (q *Queue) updateProgress(job *Job, completed, total int, bytesDelta int64) {
	q.mu.Lock()
	defer q.mu.Unlock()
	job.CompletedItems = completed
	if total > 0 {
		job.TotalItems = total
	}
	job.BytesDownloaded += bytesDelta
	if elapsed := time.Since(job.StartedAt).Seconds(); elapsed > 0.05 && job.BytesDownloaded > 0 {
		job.SpeedBps = float64(job.BytesDownloaded) / elapsed
	}
}

func (q *Queue) markFinished(job *Job, err error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	job.cancel = nil
	job.DoneAt = time.Now().UTC()
	job.SpeedBps = 0
	if job.Status == JobCancelled || errors.Is(err, context.Canceled) {
		job.Status = JobCancelled
		job.Error = "Cancelled by user"
		return
	}
	if err != nil {
		job.Status = JobFailed
		job.Error = err.Error()
		return
	}
	job.Status = JobDone
	if job.TotalItems > 0 {
		job.CompletedItems = job.TotalItems
	}
}

func (q *Queue) Cancel(id int) error {
	q.mu.Lock()
	defer q.mu.Unlock()

	job, ok := q.byID[id]
	if !ok {
		return ErrJobNotFound
	}
	switch job.Status {
	case JobPending:
		job.Status = JobCancelled
		job.DoneAt = time.Now().UTC()
		job.Error = "Cancelled before start"
		return nil
	case JobRunning:
		job.Status = JobCancelled
		if job.cancel != nil {
			job.cancel()
		}
		return nil
	default:
		return ErrJobNotCancel
	}
}

func (q *Queue) Retry(id int) (int, error) {
	q.mu.Lock()
	job, ok := q.byID[id]
	if !ok {
		q.mu.Unlock()
		return 0, ErrJobNotFound
	}
	if job.Status != JobFailed && job.Status != JobCancelled {
		q.mu.Unlock()
		return 0, ErrJobNotRetryable
	}
	copyJob := &Job{
		Type:     job.Type,
		Chapter:  job.Chapter,
		SrcDir:   job.SrcDir,
		DestDir:  job.DestDir,
		DestFile: job.DestFile,
	}
	q.mu.Unlock()

	newID, created := q.EnqueueUnique(copyJob)
	if !created {
		return newID, fmt.Errorf("queue: job already active (#%d)", newID)
	}
	return newID, nil
}

func (q *Queue) RetryAllFailed() int {
	q.mu.RLock()
	var toRetry []*Job
	for _, j := range q.all {
		if j.Status == JobFailed {
			toRetry = append(toRetry, &Job{
				Type:     j.Type,
				Chapter:  j.Chapter,
				SrcDir:   j.SrcDir,
				DestDir:  j.DestDir,
				DestFile: j.DestFile,
			})
		}
	}
	q.mu.RUnlock()

	count := 0
	for _, job := range toRetry {
		if _, created := q.EnqueueUnique(job); created {
			count++
		}
	}
	return count
}

func (q *Queue) ClearFinished() int {
	q.mu.Lock()
	defer q.mu.Unlock()

	kept := make([]*Job, 0, len(q.all))
	removed := 0
	for _, j := range q.all {
		if j.Status == JobDone || j.Status == JobCancelled {
			delete(q.byID, j.ID)
			removed++
			continue
		}
		kept = append(kept, j)
	}
	q.all = kept
	return removed
}

func (q *Queue) Pause() {
	q.mu.Lock()
	q.paused = true
	q.mu.Unlock()
}

func (q *Queue) Resume() {
	q.mu.Lock()
	q.paused = false
	q.cond.Broadcast()
	q.mu.Unlock()
}

func (q *Queue) IsPaused() bool {
	q.mu.RLock()
	defer q.mu.RUnlock()
	return q.paused
}

// Jobs returns immutable value snapshots safe for concurrent reading.
func (q *Queue) Jobs() []*Job {
	q.mu.RLock()
	defer q.mu.RUnlock()

	snapshots := make([]*Job, len(q.all))
	for i, j := range q.all {
		cp := *j
		cp.cancel = nil
		snapshots[i] = &cp
	}
	return snapshots
}

func (q *Queue) Job(id int) (*Job, bool) {
	q.mu.RLock()
	defer q.mu.RUnlock()

	j, ok := q.byID[id]
	if !ok {
		return nil, false
	}
	cp := *j
	cp.cancel = nil
	return &cp, true
}

func (q *Queue) Stats() QueueStats {
	q.mu.RLock()
	defer q.mu.RUnlock()

	s := QueueStats{
		Total:  len(q.all),
		Paused: q.paused,
	}
	for _, j := range q.all {
		switch j.Status {
		case JobPending:
			s.Pending++
		case JobRunning:
			s.Running++
			s.TotalSpeed += j.SpeedBps
			s.ActiveBytes += j.BytesDownloaded
		case JobDone:
			s.Done++
		case JobFailed:
			s.Failed++
		case JobCancelled:
			s.Cancelled++
		}
	}
	return s
}
