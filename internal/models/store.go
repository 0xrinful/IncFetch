package models

import "time"

type MangaRecord struct {
	Manga
	CoverPath string
	AddedAt   time.Time
	UpdatedAt time.Time
}

type MangaSummary struct {
	MangaRecord
	TotalChapters      int
	DownloadedChapters int
	PackedChapters     int
	ReadChapters       int
}

type ChapterRecord struct {
	Chapter
	RawPath       string
	OptimizedPath string
	CBZPath       string
	Downloaded    bool
	DownloadedAt  time.Time
	Optimized     bool
	OptimizedAt   time.Time
	Packed        bool
	PackedAt      time.Time
}

type User struct {
	ID           int
	Email        string
	PasswordHash string
	CreatedAt    time.Time
}

type SourceCookie struct {
	Name      string    `json:"name"`
	Value     string    `json:"value"`
	Domain    string    `json:"domain,omitempty"`
	Path      string    `json:"path,omitempty"`
	ExpiresAt time.Time `json:"expires_at,omitempty"`
}

type SourceSession struct {
	SourceID  string
	Cookies   []SourceCookie
	UserAgent string
	ExpiresAt time.Time
	UpdatedAt time.Time
}

type AppSettings struct {
	DownloadConcurrency int
	PageConcurrency     int
	AutoOptimize        bool
	AutoPack            bool
	OptimizeQuality     int
}

func DefaultSettings() AppSettings {
	return AppSettings{
		DownloadConcurrency: 3,
		PageConcurrency:     6,
		AutoOptimize:        true,
		AutoPack:            true,
		OptimizeQuality:     78,
	}
}

func (s AppSettings) Normalized() AppSettings {
	if s.DownloadConcurrency < 1 {
		s.DownloadConcurrency = 1
	} else if s.DownloadConcurrency > 10 {
		s.DownloadConcurrency = 10
	}

	if s.PageConcurrency < 1 {
		s.PageConcurrency = 1
	} else if s.PageConcurrency > 16 {
		s.PageConcurrency = 16
	}

	if s.OptimizeQuality < 50 {
		s.OptimizeQuality = 50
	} else if s.OptimizeQuality > 95 {
		s.OptimizeQuality = 95
	}

	return s
}
