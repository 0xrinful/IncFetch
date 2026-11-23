package queue

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/0xrinful/InkFetch/internal/models"
)

func TestQueueDeduplicateCancelRetryAndClear(t *testing.T) {
	q := NewQueue()

	ch := models.Chapter{SourceID: "teamx", MangaSlug: "demo", Number: 1}
	id1, created1 := q.EnqueueUnique(&Job{Type: JobDownload, Chapter: ch})
	if !created1 || id1 != 1 {
		t.Fatalf("expected first enqueue created=true id=1, got created=%v id=%d", created1, id1)
	}

	id2, created2 := q.EnqueueUnique(&Job{Type: JobDownload, Chapter: ch})
	if created2 || id2 != id1 {
		t.Fatalf("expected duplicate enqueue to reuse id=%d created=false, got id=%d created=%v", id1, id2, created2)
	}

	if err := q.Cancel(id1); err != nil {
		t.Fatalf("Cancel failed: %v", err)
	}
	snap, ok := q.Job(id1)
	if !ok || snap.Status != JobCancelled {
		t.Fatalf("expected JobCancelled, got ok=%v status=%v", ok, snap.Status)
	}

	retryID, err := q.Retry(id1)
	if err != nil || retryID == id1 {
		t.Fatalf("Retry failed: id=%d err=%v", retryID, err)
	}

	// Simulate failure on retried job, then RetryAllFailed.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	job := q.next(ctx, JobDownload)
	if job == nil || job.ID != retryID {
		t.Fatalf("expected job %d from next, got %+v", retryID, job)
	}
	if !q.markRunning(job, cancel) {
		t.Fatalf("markRunning returned false")
	}
	q.updateProgress(job, 5, 10, 51200)
	q.markFinished(job, errors.New("network timeout"))

	retried := q.RetryAllFailed()
	if retried != 1 {
		t.Fatalf("expected RetryAllFailed=1, got %d", retried)
	}

	cleared := q.ClearFinished()
	if cleared != 1 {
		t.Fatalf("expected ClearFinished to remove 1 cancelled job, got %d", cleared)
	}

	stats := q.Stats()
	if stats.Pending != 1 || stats.Failed != 1 {
		t.Fatalf("unexpected queue stats: %+v", stats)
	}
}

func TestQueuePauseAndResume(t *testing.T) {
	q := NewQueue()
	q.Pause()
	if !q.IsPaused() {
		t.Fatalf("expected queue to be paused")
	}

	q.Enqueue(&Job{
		Type:    JobDownload,
		Chapter: models.Chapter{SourceID: "azora", MangaSlug: "test", Number: 2},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	gotCh := make(chan *Job, 1)
	go func() {
		gotCh <- q.next(ctx, JobDownload)
	}()

	select {
	case j := <-gotCh:
		t.Fatalf("expected next() to block while paused, but got job %+v", j)
	case <-time.After(60 * time.Millisecond):
	}

	q.Resume()

	select {
	case j := <-gotCh:
		if j == nil || j.Chapter.Number != 2 {
			t.Fatalf("unexpected job after resume: %+v", j)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("timed out waiting for job after Resume")
	}
}
