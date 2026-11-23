package service

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/0xrinful/InkFetch/internal/models"
	"github.com/0xrinful/InkFetch/internal/queue"
	"github.com/0xrinful/InkFetch/internal/sources"
)

func (s *Service) Library(ctx context.Context, userID int) ([]models.MangaRecord, error) {
	return s.db.UserMangas(userID)
}

func (s *Service) LibrarySummaries(ctx context.Context, userID int) ([]models.MangaSummary, error) {
	return s.db.UserMangaSummaries(userID)
}

type MangaPageResult struct {
	Manga      *models.MangaRecord
	Chapters   []models.ChapterRecord
	ReadMarks  []float64
	IsFavorite bool
}

type DeleteRequest struct {
	Raw       bool `json:"raw"`
	Optimized bool `json:"optimized"`
	Packed    bool `json:"packed"`
}

func (s *Service) MangaPage(
	ctx context.Context,
	userID int,
	slug, sourceID string,
) (*MangaPageResult, error) {
	manga, err := s.db.Manga(slug, sourceID)
	if err != nil {
		return nil, err
	}
	if manga == nil {
		return nil, ErrNotFound
	}

	chapters, err := s.db.Chapters(slug, sourceID)
	if err != nil {
		return nil, err
	}

	readMarks, err := s.db.ReadMarks(userID, slug, sourceID)
	if err != nil {
		return nil, err
	}

	isFav, err := s.db.IsFavorite(userID, slug, sourceID)
	if err != nil {
		return nil, err
	}

	return &MangaPageResult{
		Manga:      manga,
		Chapters:   chapters,
		ReadMarks:  readMarks,
		IsFavorite: isFav,
	}, nil
}

func (s *Service) SourceLatest(
	ctx context.Context,
	sourceID string,
	page, size int,
) ([]models.Manga, error) {
	src, ok := s.registry.Source(sourceID)
	if !ok {
		return nil, ErrUnknownSource
	}
	return src.Latest(ctx, page, size)
}

func (s *Service) SourceSearch(
	ctx context.Context,
	sourceID, query string,
	page, size int,
) ([]models.Manga, error) {
	src, ok := s.registry.Source(sourceID)
	if !ok {
		return nil, ErrUnknownSource
	}
	return src.Search(ctx, query, page, size)
}

func (s *Service) SourceManga(ctx context.Context, sourceID, slug string) (*models.Manga, error) {
	src, ok := s.registry.Source(sourceID)
	if !ok {
		return nil, ErrUnknownSource
	}
	return src.Manga(ctx, slug)
}

func (s *Service) Sources() []sources.SourceInfo {
	return s.registry.Sources()
}

func (s *Service) SourceInfo(sourceID string) (sources.SourceInfo, bool) {
	src, ok := s.registry.Source(sourceID)
	if !ok {
		return sources.SourceInfo{}, false
	}
	return src.Info(), true
}

func extractExt(rawURL string) string {
	if i := strings.IndexByte(rawURL, '?'); i != -1 {
		rawURL = rawURL[:i]
	}
	ext := strings.ToLower(filepath.Ext(rawURL))
	switch ext {
	case ".jpg", ".jpeg", ".png", ".webp", ".gif":
		return ext
	default:
		return ".jpg"
	}
}

func (s *Service) ImportManga(ctx context.Context, sourceID, slug string) error {
	src, ok := s.registry.Source(sourceID)
	if !ok {
		return ErrUnknownSource
	}

	manga, err := src.Manga(ctx, slug)
	if err != nil {
		return fmt.Errorf("service: scrape manga: %w", err)
	}

	dl, ok := s.registry.Downloader(manga.SourceID)
	if !ok {
		return ErrUnknownSource
	}

	coverPath := ""
	if strings.TrimSpace(manga.CoverURL) != "" {
		candidate := s.files.CoverPath(*manga, extractExt(manga.CoverURL))
		if err := s.files.EnsureDir(filepath.Dir(candidate)); err != nil {
			return fmt.Errorf("service: ensure cover dir: %w", err)
		}
		if err := dl.DownloadCover(ctx, manga.CoverURL, candidate); err != nil {
			slog.Warn("cover download failed during import, continuing without local cover",
				"source", sourceID, "slug", slug, "err", err)
		} else {
			coverPath = candidate
		}
	}

	now := time.Now().UTC()
	if err := s.db.SaveManga(models.MangaRecord{
		Manga:     *manga,
		AddedAt:   now,
		UpdatedAt: now,
		CoverPath: coverPath,
	}); err != nil {
		return fmt.Errorf("service: save manga: %w", err)
	}

	if manga.Chapters == nil {
		chapters, err := src.Chapters(ctx, slug)
		if err != nil {
			return fmt.Errorf("service: scrape chapters: %w", err)
		}
		manga.Chapters = chapters
	}

	for _, ch := range manga.Chapters {
		if err := s.db.SaveChapter(models.ChapterRecord{Chapter: ch}); err != nil {
			return fmt.Errorf("service: save chapter: %w", err)
		}
	}

	return nil
}

func (s *Service) RefreshManga(ctx context.Context, sourceID, slug string) error {
	if ctx == nil {
		ctx = context.Background()
	}

	manga, err := s.SourceManga(ctx, sourceID, slug)
	if err != nil {
		return fmt.Errorf("service: scrape manga: %w", err)
	}
	if manga == nil {
		return ErrNotFound
	}

	existing, err := s.db.Manga(slug, sourceID)
	if err != nil {
		return fmt.Errorf("service: fetch manga record: %w", err)
	}

	if manga.Chapters == nil {
		src, ok := s.registry.Source(sourceID)
		if !ok {
			return ErrUnknownSource
		}
		chapters, err := src.Chapters(ctx, slug)
		if err != nil {
			return fmt.Errorf("service: scrape chapters: %w", err)
		}
		manga.Chapters = chapters
	}

	addedAt := time.Now().UTC()
	coverPath := ""
	if existing != nil {
		addedAt = existing.AddedAt
		coverPath = existing.CoverPath
	}

	if coverPath == "" && strings.TrimSpace(manga.CoverURL) != "" {
		if dl, ok := s.registry.Downloader(manga.SourceID); ok {
			candidate := s.files.CoverPath(*manga, extractExt(manga.CoverURL))
			if err := s.files.EnsureDir(filepath.Dir(candidate)); err == nil {
				if err := dl.DownloadCover(ctx, manga.CoverURL, candidate); err == nil {
					coverPath = candidate
				}
			}
		}
	}

	if err := s.db.SaveManga(models.MangaRecord{
		Manga:     *manga,
		CoverPath: coverPath,
		AddedAt:   addedAt,
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		return fmt.Errorf("service: save manga: %w", err)
	}

	for _, ch := range manga.Chapters {
		existingChapter, err := s.db.Chapter(ch.MangaSlug, ch.SourceID, ch.Number)
		if err != nil {
			return fmt.Errorf("service: fetch chapter: %w", err)
		}
		if existingChapter == nil {
			if err := s.db.SaveChapter(models.ChapterRecord{Chapter: ch}); err != nil {
				return fmt.Errorf("service: save chapter: %w", err)
			}
			continue
		}

		existingChapter.Chapter = ch
		if err := s.db.SaveChapter(*existingChapter); err != nil {
			return fmt.Errorf("service: save chapter: %w", err)
		}
	}

	return nil
}

func (s *Service) DeleteMangaFiles(sourceID, slug string, req DeleteRequest) error {
	chapters, err := s.db.Chapters(slug, sourceID)
	if err != nil {
		return fmt.Errorf("service: fetch chapters: %w", err)
	}

	for _, ch := range chapters {
		if req.Raw {
			if err := os.RemoveAll(s.files.ChapterDir(ch.Chapter)); err != nil {
				return fmt.Errorf("service: remove raw: %w", err)
			}
			if err := s.db.MarkUndownloaded(ch.Chapter); err != nil {
				return fmt.Errorf("service: mark undownloaded: %w", err)
			}
		}
		if req.Optimized {
			if err := os.RemoveAll(s.files.OptimizedDir(ch.Chapter)); err != nil {
				return fmt.Errorf("service: remove optimized: %w", err)
			}
			if err := s.db.MarkUnoptimized(ch.Chapter); err != nil {
				return fmt.Errorf("service: mark unoptimized: %w", err)
			}
		}
		if req.Packed {
			if err := os.RemoveAll(s.files.CBZPath(ch.Chapter)); err != nil {
				return fmt.Errorf("service: remove packed: %w", err)
			}
			if err := s.db.MarkUnpacked(ch.Chapter); err != nil {
				return fmt.Errorf("service: mark unpacked: %w", err)
			}
		}
	}

	return nil
}

func (s *Service) Favorite(ctx context.Context, userID int, sourceID, slug string) error {
	existing, err := s.db.Manga(slug, sourceID)
	if err != nil {
		return err
	}

	if existing == nil {
		if err := s.ImportManga(ctx, sourceID, slug); err != nil {
			return err
		}
	}

	return s.db.AddFavorite(userID, slug, sourceID)
}

func (s *Service) Unfavorite(ctx context.Context, userID int, sourceID, slug string) error {
	return s.db.RemoveFavorite(userID, slug, sourceID)
}

func (s *Service) DownloadChapter(
	ctx context.Context,
	sourceID string,
	chapter models.Chapter,
) (int, error) {
	id, _, err := s.downloadChapterUnique(chapter)
	return id, err
}

func (s *Service) downloadChapterUnique(chapter models.Chapter) (int, bool, error) {
	destDir := s.files.ChapterDir(chapter)
	if err := s.files.EnsureDir(destDir); err != nil {
		return 0, false, fmt.Errorf("service: ensure dir: %w", err)
	}

	jobID, created := s.queue.EnqueueUnique(&queue.Job{
		Type:    queue.JobDownload,
		Chapter: chapter,
		DestDir: destDir,
	})
	return jobID, created, nil
}

func (s *Service) DownloadRange(
	ctx context.Context,
	sourceID, mangaSlug string,
	r models.ChapterRange,
) ([]int, error) {
	chapters, err := s.db.Chapters(mangaSlug, sourceID)
	if err != nil {
		return nil, err
	}

	var jobIDs []int
	for _, ch := range chapters {
		if !r.Contains(ch.Number) {
			continue
		}
		if ch.Downloaded && !r.Force {
			continue
		}

		id, created, err := s.downloadChapterUnique(ch.Chapter)
		if err != nil {
			return nil, err
		}
		if created {
			jobIDs = append(jobIDs, id)
		}
	}

	return jobIDs, nil
}

func (s *Service) DownloadChapters(
	ctx context.Context,
	sourceID, mangaSlug string,
	numbers []float64,
	force bool,
) (int, error) {
	selected := floatSet(numbers)
	chapters, err := s.db.Chapters(mangaSlug, sourceID)
	if err != nil {
		return 0, err
	}

	queued := 0
	for _, ch := range chapters {
		if !selected[ch.Number] {
			continue
		}
		if ch.Downloaded && !force {
			continue
		}
		_, created, err := s.downloadChapterUnique(ch.Chapter)
		if err != nil {
			return queued, err
		}
		if created {
			queued++
		}
	}
	return queued, nil
}

func (s *Service) OptimizeChapter(ctx context.Context, chapter models.Chapter) (int, error) {
	ch, err := s.db.Chapter(chapter.MangaSlug, chapter.SourceID, chapter.Number)
	if err != nil {
		return 0, err
	}
	if ch == nil || !ch.Downloaded {
		return 0, ErrNotDownloaded
	}
	destDir := s.files.OptimizedDir(chapter)
	if err := s.files.EnsureDir(destDir); err != nil {
		return 0, fmt.Errorf("service: ensure optimized dir: %w", err)
	}

	jobID, _ := s.queue.EnqueueUnique(&queue.Job{
		Type:    queue.JobOptimize,
		Chapter: chapter,
		SrcDir:  ch.RawPath,
		DestDir: destDir,
	})

	return jobID, nil
}

func (s *Service) OptimizeRange(
	ctx context.Context,
	sourceID, mangaSlug string,
	r models.ChapterRange,
) ([]int, error) {
	chapters, err := s.db.Chapters(mangaSlug, sourceID)
	if err != nil {
		return nil, err
	}

	var jobIDs []int
	for _, ch := range chapters {
		if !r.Contains(ch.Number) || (ch.Optimized && !r.Force) || !ch.Downloaded {
			continue
		}

		destDir := s.files.OptimizedDir(ch.Chapter)
		if err := s.files.EnsureDir(destDir); err != nil {
			return nil, err
		}

		id, created := s.queue.EnqueueUnique(&queue.Job{
			Type:    queue.JobOptimize,
			Chapter: ch.Chapter,
			SrcDir:  ch.RawPath,
			DestDir: destDir,
		})
		if created {
			jobIDs = append(jobIDs, id)
		}
	}

	return jobIDs, nil
}

func (s *Service) OptimizeChapters(
	ctx context.Context,
	sourceID, mangaSlug string,
	numbers []float64,
	force bool,
) (int, error) {
	selected := floatSet(numbers)
	chapters, err := s.db.Chapters(mangaSlug, sourceID)
	if err != nil {
		return 0, err
	}

	queued := 0
	for _, ch := range chapters {
		if !selected[ch.Number] || !ch.Downloaded || (ch.Optimized && !force) {
			continue
		}
		destDir := s.files.OptimizedDir(ch.Chapter)
		if err := s.files.EnsureDir(destDir); err != nil {
			return queued, err
		}
		_, created := s.queue.EnqueueUnique(&queue.Job{
			Type:    queue.JobOptimize,
			Chapter: ch.Chapter,
			SrcDir:  ch.RawPath,
			DestDir: destDir,
		})
		if created {
			queued++
		}
	}
	return queued, nil
}

func (s *Service) PackChapter(ctx context.Context, chapter models.Chapter) (int, error) {
	ch, err := s.db.Chapter(chapter.MangaSlug, chapter.SourceID, chapter.Number)
	if err != nil {
		return 0, err
	}
	if ch == nil || !ch.Downloaded {
		return 0, ErrNotDownloaded
	}

	srcDir := ch.RawPath
	if ch.Optimized && ch.OptimizedPath != "" {
		srcDir = ch.OptimizedPath
	}

	jobID, _ := s.queue.EnqueueUnique(&queue.Job{
		Type:     queue.JobPack,
		Chapter:  chapter,
		SrcDir:   srcDir,
		DestFile: s.files.CBZPath(chapter),
	})

	return jobID, nil
}

func (s *Service) PackRange(
	ctx context.Context,
	sourceID, mangaSlug string,
	r models.ChapterRange,
) ([]int, error) {
	chapters, err := s.db.Chapters(mangaSlug, sourceID)
	if err != nil {
		return nil, err
	}

	var jobIDs []int
	for _, ch := range chapters {
		if !r.Contains(ch.Number) {
			continue
		}
		if ch.Packed && !r.Force {
			continue
		}
		if !ch.Downloaded {
			continue
		}

		srcDir := ch.RawPath
		if ch.Optimized && ch.OptimizedPath != "" {
			srcDir = ch.OptimizedPath
		}

		id, created := s.queue.EnqueueUnique(&queue.Job{
			Type:     queue.JobPack,
			Chapter:  ch.Chapter,
			SrcDir:   srcDir,
			DestFile: s.files.CBZPath(ch.Chapter),
		})
		if created {
			jobIDs = append(jobIDs, id)
		}
	}

	return jobIDs, nil
}

func (s *Service) PackChapters(
	ctx context.Context,
	sourceID, mangaSlug string,
	numbers []float64,
	force bool,
) (int, error) {
	selected := floatSet(numbers)
	chapters, err := s.db.Chapters(mangaSlug, sourceID)
	if err != nil {
		return 0, err
	}

	queued := 0
	for _, ch := range chapters {
		if !selected[ch.Number] || !ch.Downloaded || (ch.Packed && !force) {
			continue
		}
		srcDir := ch.RawPath
		if ch.Optimized && ch.OptimizedPath != "" {
			srcDir = ch.OptimizedPath
		}
		_, created := s.queue.EnqueueUnique(&queue.Job{
			Type:     queue.JobPack,
			Chapter:  ch.Chapter,
			SrcDir:   srcDir,
			DestFile: s.files.CBZPath(ch.Chapter),
		})
		if created {
			queued++
		}
	}
	return queued, nil
}

func (s *Service) PackManga(
	ctx context.Context,
	sourceID, mangaSlug string,
	r models.ChapterRange,
) (string, error) {
	manga, err := s.db.Manga(mangaSlug, sourceID)
	if err != nil {
		return "", fmt.Errorf("service: fetch manga record: %w", err)
	}
	if manga == nil {
		return "", ErrNotFound
	}

	cbzPaths, err := s.files.CBZRange(manga.Manga, r)
	if err != nil {
		return "", fmt.Errorf("service: fetch cbz paths: %w", err)
	}
	if len(cbzPaths) == 0 {
		return "", fmt.Errorf("no packed chapters in selected range — pack chapters first")
	}

	tmpFile, err := os.CreateTemp("", "inkfetch-pack-*.zip")
	if err != nil {
		return "", fmt.Errorf("service: create temp file: %w", err)
	}
	destZip := tmpFile.Name()
	tmpFile.Close()

	if err := s.packer.PackManga(ctx, manga.Manga, cbzPaths, manga.CoverPath, destZip); err != nil {
		os.Remove(destZip)
		return "", fmt.Errorf("service: failed compiling manga pack: %w", err)
	}

	return destZip, nil
}

func (s *Service) Jobs() []*queue.Job {
	return s.queue.Jobs()
}

func (s *Service) Job(id int) (*queue.Job, bool) {
	return s.queue.Job(id)
}

func (s *Service) MarkRead(
	ctx context.Context,
	userID int,
	mangaSlug, sourceID string,
	number float64,
) error {
	return s.db.MarkRead(userID, mangaSlug, sourceID, number)
}

func (s *Service) MarkUnread(
	ctx context.Context,
	userID int,
	mangaSlug, sourceID string,
	number float64,
) error {
	return s.db.MarkUnread(userID, mangaSlug, sourceID, number)
}

func (s *Service) MarkChaptersRead(
	ctx context.Context,
	userID int,
	sourceID, mangaSlug string,
	numbers []float64,
) error {
	for _, n := range numbers {
		if err := s.db.MarkRead(userID, mangaSlug, sourceID, n); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) MarkChaptersUnread(
	ctx context.Context,
	userID int,
	sourceID, mangaSlug string,
	numbers []float64,
) error {
	for _, n := range numbers {
		if err := s.db.MarkUnread(userID, mangaSlug, sourceID, n); err != nil {
			return err
		}
	}
	return nil
}

func floatSet(nums []float64) map[float64]bool {
	set := make(map[float64]bool, len(nums))
	for _, n := range nums {
		set[n] = true
	}
	return set
}
