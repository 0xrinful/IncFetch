package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	_ "modernc.org/sqlite"

	"github.com/0xrinful/InkFetch/internal/models"
)

type DB struct {
	sql *sql.DB
}

func New(path string) (*DB, error) {
	conn, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("db: open: %w", err)
	}

	db := &DB{sql: conn}
	if err := db.init(); err != nil {
		return nil, err
	}

	return db, nil
}

func (db *DB) Close() error {
	if db == nil || db.sql == nil {
		return nil
	}
	return db.sql.Close()
}

func (db *DB) init() error {
	pragmas := []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA synchronous=NORMAL",
		"PRAGMA foreign_keys=ON",
		"PRAGMA busy_timeout=5000",
	}
	for _, p := range pragmas {
		if _, err := db.sql.Exec(p); err != nil {
			return fmt.Errorf("db: pragma %s: %w", p, err)
		}
	}
	return db.migrate()
}

func (db *DB) migrate() error {
	_, err := db.sql.Exec(`
        CREATE TABLE IF NOT EXISTS users (
            id            INTEGER PRIMARY KEY AUTOINCREMENT,
            email         TEXT NOT NULL UNIQUE,
            password_hash TEXT NOT NULL,
            created_at    DATETIME NOT NULL
        );

        CREATE TABLE IF NOT EXISTS manga (
            slug        TEXT NOT NULL,
            source_id   TEXT NOT NULL,
            title       TEXT NOT NULL,
            description TEXT,
            cover_url   TEXT,
            cover_path  TEXT,
            status      TEXT,
            genres      TEXT,
            added_at    DATETIME NOT NULL,
            updated_at  DATETIME NOT NULL,
            PRIMARY KEY (slug, source_id)
        );

        CREATE TABLE IF NOT EXISTS chapters (
            manga_slug     TEXT NOT NULL,
            source_id      TEXT NOT NULL,
            number         REAL NOT NULL,
            title          TEXT,
            url            TEXT,
            released_at    DATETIME,
            raw_path       TEXT,
            optimized_path TEXT,
            cbz_path       TEXT,
            downloaded     INTEGER NOT NULL DEFAULT 0,
            downloaded_at  DATETIME,
            optimized      INTEGER NOT NULL DEFAULT 0,
            optimized_at   DATETIME,
            packed         INTEGER NOT NULL DEFAULT 0,
            packed_at      DATETIME,
            PRIMARY KEY (manga_slug, source_id, number),
            FOREIGN KEY (manga_slug, source_id) REFERENCES manga(slug, source_id)
        );

        CREATE TABLE IF NOT EXISTS favorites (
            user_id    INTEGER NOT NULL,
            manga_slug TEXT NOT NULL,
            source_id  TEXT NOT NULL,
            added_at   DATETIME NOT NULL,
            PRIMARY KEY (user_id, manga_slug, source_id),
            FOREIGN KEY (user_id) REFERENCES users(id),
            FOREIGN KEY (manga_slug, source_id) REFERENCES manga(slug, source_id)
        );

        CREATE TABLE IF NOT EXISTS read_marks (
            user_id    INTEGER NOT NULL,
            manga_slug TEXT NOT NULL,
            source_id  TEXT NOT NULL,
            number     REAL NOT NULL,
            read_at    DATETIME NOT NULL,
            PRIMARY KEY (user_id, manga_slug, source_id, number),
            FOREIGN KEY (user_id) REFERENCES users(id)
        );

        CREATE TABLE IF NOT EXISTS settings (
            key   TEXT PRIMARY KEY,
            value TEXT NOT NULL
        );

        CREATE TABLE IF NOT EXISTS source_sessions (
            source_id  TEXT PRIMARY KEY,
            cookies    TEXT NOT NULL,
            user_agent TEXT NOT NULL,
            expires_at DATETIME NOT NULL,
            updated_at DATETIME NOT NULL
        );
    `)
	return err
}

// --- Source sessions ---

func (db *DB) SourceSession(sourceID string) (*models.SourceSession, error) {
	row := db.sql.QueryRow(`
        SELECT source_id, cookies, user_agent, expires_at, updated_at
        FROM source_sessions
        WHERE source_id = ?
    `, sourceID)

	var (
		s           models.SourceSession
		cookiesJSON string
	)
	err := row.Scan(&s.SourceID, &cookiesJSON, &s.UserAgent, &s.ExpiresAt, &s.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	if cookiesJSON != "" {
		if err := json.Unmarshal([]byte(cookiesJSON), &s.Cookies); err != nil {
			return nil, fmt.Errorf("db: decode source session cookies: %w", err)
		}
	}
	s.ExpiresAt = s.ExpiresAt.UTC()
	s.UpdatedAt = s.UpdatedAt.UTC()
	return &s, nil
}

func (db *DB) SaveSourceSession(session models.SourceSession) error {
	cookies := session.Cookies
	if cookies == nil {
		cookies = []models.SourceCookie{}
	}
	raw, err := json.Marshal(cookies)
	if err != nil {
		return fmt.Errorf("db: encode source session cookies: %w", err)
	}

	updatedAt := session.UpdatedAt
	if updatedAt.IsZero() {
		updatedAt = time.Now().UTC()
	}

	_, err = db.sql.Exec(`
        INSERT INTO source_sessions (source_id, cookies, user_agent, expires_at, updated_at)
        VALUES (?, ?, ?, ?, ?)
        ON CONFLICT (source_id) DO UPDATE SET
            cookies    = excluded.cookies,
            user_agent = excluded.user_agent,
            expires_at = excluded.expires_at,
            updated_at = excluded.updated_at
    `, session.SourceID, string(raw), session.UserAgent, session.ExpiresAt.UTC(), updatedAt.UTC())
	return err
}

func (db *DB) DeleteSourceSession(sourceID string) error {
	_, err := db.sql.Exec(`DELETE FROM source_sessions WHERE source_id = ?`, sourceID)
	return err
}

func (db *DB) AllSourceSessions() ([]models.SourceSession, error) {
	rows, err := db.sql.Query(`
        SELECT source_id, cookies, user_agent, expires_at, updated_at
        FROM source_sessions
        ORDER BY source_id ASC
    `)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var sessions []models.SourceSession
	for rows.Next() {
		var (
			s           models.SourceSession
			cookiesJSON string
		)
		if err := rows.Scan(&s.SourceID, &cookiesJSON, &s.UserAgent, &s.ExpiresAt, &s.UpdatedAt); err != nil {
			return nil, err
		}
		if cookiesJSON != "" {
			if err := json.Unmarshal([]byte(cookiesJSON), &s.Cookies); err != nil {
				return nil, fmt.Errorf("db: decode source session cookies: %w", err)
			}
		}
		s.ExpiresAt = s.ExpiresAt.UTC()
		s.UpdatedAt = s.UpdatedAt.UTC()
		sessions = append(sessions, s)
	}
	return sessions, rows.Err()
}

// --- Settings ---

func (db *DB) LoadSettings() (models.AppSettings, error) {
	cfg := models.DefaultSettings()

	rows, err := db.sql.Query(`SELECT key, value FROM settings`)
	if err != nil {
		return cfg, err
	}
	defer rows.Close()

	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return cfg, err
		}
		switch k {
		case "download_concurrency":
			if n, err := strconv.Atoi(v); err == nil {
				cfg.DownloadConcurrency = n
			}
		case "page_concurrency":
			if n, err := strconv.Atoi(v); err == nil {
				cfg.PageConcurrency = n
			}
		case "auto_optimize":
			if b, err := strconv.ParseBool(v); err == nil {
				cfg.AutoOptimize = b
			}
		case "auto_pack":
			if b, err := strconv.ParseBool(v); err == nil {
				cfg.AutoPack = b
			}
		case "optimize_quality":
			if n, err := strconv.Atoi(v); err == nil {
				cfg.OptimizeQuality = n
			}
		}
	}

	return cfg.Normalized(), rows.Err()
}

func (db *DB) SaveSettings(cfg models.AppSettings) error {
	cfg = cfg.Normalized()
	pairs := map[string]string{
		"download_concurrency": strconv.Itoa(cfg.DownloadConcurrency),
		"page_concurrency":     strconv.Itoa(cfg.PageConcurrency),
		"auto_optimize":        strconv.FormatBool(cfg.AutoOptimize),
		"auto_pack":            strconv.FormatBool(cfg.AutoPack),
		"optimize_quality":     strconv.Itoa(cfg.OptimizeQuality),
	}

	tx, err := db.sql.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(`
		INSERT INTO settings (key, value) VALUES (?, ?)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value
	`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for k, v := range pairs {
		if _, err := stmt.Exec(k, v); err != nil {
			return err
		}
	}

	return tx.Commit()
}

// --- Manga ---

func (db *DB) SaveManga(m models.MangaRecord) error {
	genres, err := json.Marshal(m.Genres)
	if err != nil {
		return err
	}
	_, err = db.sql.Exec(`
        INSERT INTO manga (slug, source_id, title, description, cover_url, cover_path, status, genres, added_at, updated_at)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
        ON CONFLICT (slug, source_id) DO UPDATE SET
            title       = excluded.title,
            description = excluded.description,
            cover_url   = excluded.cover_url,
            cover_path  = excluded.cover_path,
            status      = excluded.status,
            genres      = excluded.genres,
            updated_at  = excluded.updated_at
    `, m.Slug, m.SourceID, m.Title, m.Description, m.CoverURL,
		m.CoverPath, m.Status, string(genres), m.AddedAt, m.UpdatedAt)
	return err
}

func (db *DB) Manga(slug, sourceID string) (*models.MangaRecord, error) {
	row := db.sql.QueryRow(`
        SELECT slug, source_id, title, description, cover_url, cover_path, status, genres, added_at, updated_at
        FROM manga WHERE slug = ? AND source_id = ?
    `, slug, sourceID)

	var m models.MangaRecord
	var genres string
	err := row.Scan(
		&m.Slug, &m.SourceID, &m.Title, &m.Description,
		&m.CoverURL, &m.CoverPath, &m.Status, &genres,
		&m.AddedAt, &m.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(genres), &m.Genres)
	return &m, nil
}

func (db *DB) UserMangas(userID int) ([]models.MangaRecord, error) {
	rows, err := db.sql.Query(`
        SELECT m.slug, m.source_id, m.title, m.description, m.cover_url,
               m.cover_path, m.status, m.genres, m.added_at, m.updated_at
        FROM manga m
        JOIN favorites f ON f.manga_slug = m.slug AND f.source_id = m.source_id
        WHERE f.user_id = ?
        ORDER BY m.title
    `, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanMangas(rows)
}

func (db *DB) UserMangaSummaries(userID int) ([]models.MangaSummary, error) {
	rows, err := db.sql.Query(`
        SELECT m.slug, m.source_id, m.title, m.description, m.cover_url,
               m.cover_path, m.status, m.genres, m.added_at, m.updated_at,
               COALESCE(c.total_chapters, 0),
               COALESCE(c.downloaded_chapters, 0),
               COALESCE(c.packed_chapters, 0),
               COALESCE(r.read_chapters, 0)
        FROM manga m
        JOIN favorites f ON f.manga_slug = m.slug AND f.source_id = m.source_id
        LEFT JOIN (
            SELECT manga_slug, source_id,
                   COUNT(*) AS total_chapters,
                   SUM(CASE WHEN downloaded = 1 THEN 1 ELSE 0 END) AS downloaded_chapters,
                   SUM(CASE WHEN packed = 1 THEN 1 ELSE 0 END) AS packed_chapters
            FROM chapters
            GROUP BY manga_slug, source_id
        ) c ON c.manga_slug = m.slug AND c.source_id = m.source_id
        LEFT JOIN (
            SELECT manga_slug, source_id, COUNT(*) AS read_chapters
            FROM read_marks
            WHERE user_id = ?
            GROUP BY manga_slug, source_id
        ) r ON r.manga_slug = m.slug AND r.source_id = m.source_id
        WHERE f.user_id = ?
        ORDER BY m.title COLLATE NOCASE ASC
    `, userID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var summaries []models.MangaSummary
	for rows.Next() {
		var s models.MangaSummary
		var genres string
		err := rows.Scan(
			&s.Slug, &s.SourceID, &s.Title, &s.Description,
			&s.CoverURL, &s.CoverPath, &s.Status, &genres,
			&s.AddedAt, &s.UpdatedAt,
			&s.TotalChapters, &s.DownloadedChapters, &s.PackedChapters, &s.ReadChapters,
		)
		if err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(genres), &s.Genres)
		summaries = append(summaries, s)
	}
	return summaries, rows.Err()
}

// --- Chapters ---

func (db *DB) SaveChapter(ch models.ChapterRecord) error {
	_, err := db.sql.Exec(`
        INSERT INTO chapters (manga_slug, source_id, number, title, url, released_at)
        VALUES (?, ?, ?, ?, ?, ?)
        ON CONFLICT (manga_slug, source_id, number) DO UPDATE SET
            title       = excluded.title,
            url         = excluded.url,
            released_at = excluded.released_at
    `, ch.MangaSlug, ch.SourceID, ch.Number, ch.Title, ch.URL, ch.ReleasedAt)
	return err
}

func (db *DB) Chapter(mangaSlug, sourceID string, number float64) (*models.ChapterRecord, error) {
	row := db.sql.QueryRow(`
        SELECT manga_slug, source_id, number, title, url, released_at,
               raw_path, optimized_path, cbz_path,
               downloaded, downloaded_at,
               optimized, optimized_at,
               packed, packed_at
        FROM chapters WHERE manga_slug = ? AND source_id = ? AND number = ?
    `, mangaSlug, sourceID, number)
	return scanChapter(row)
}

func (db *DB) Chapters(mangaSlug, sourceID string) ([]models.ChapterRecord, error) {
	rows, err := db.sql.Query(`
        SELECT manga_slug, source_id, number, title, url, released_at,
               raw_path, optimized_path, cbz_path,
               downloaded, downloaded_at,
               optimized, optimized_at,
               packed, packed_at
        FROM chapters WHERE manga_slug = ? AND source_id = ?
        ORDER BY number ASC
    `, mangaSlug, sourceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanChapters(rows)
}

func (db *DB) MarkDownloaded(chapter models.Chapter, rawPath string) error {
	_, err := db.sql.Exec(`
        UPDATE chapters SET
            downloaded    = 1,
            downloaded_at = ?,
            raw_path      = ?
        WHERE manga_slug = ? AND source_id = ? AND number = ?
    `, time.Now().UTC(), rawPath, chapter.MangaSlug, chapter.SourceID, chapter.Number)
	return err
}

func (db *DB) MarkOptimized(chapter models.Chapter, optimizedPath string) error {
	_, err := db.sql.Exec(`
        UPDATE chapters SET
            optimized      = 1,
            optimized_at   = ?,
            optimized_path = ?
        WHERE manga_slug = ? AND source_id = ? AND number = ?
    `, time.Now().UTC(), optimizedPath, chapter.MangaSlug, chapter.SourceID, chapter.Number)
	return err
}

func (db *DB) MarkPacked(chapter models.Chapter, cbzPath string) error {
	_, err := db.sql.Exec(`
        UPDATE chapters SET
            packed    = 1,
            packed_at = ?,
            cbz_path  = ?
        WHERE manga_slug = ? AND source_id = ? AND number = ?
    `, time.Now().UTC(), cbzPath, chapter.MangaSlug, chapter.SourceID, chapter.Number)
	return err
}

func (db *DB) MarkUndownloaded(chapter models.Chapter) error {
	_, err := db.sql.Exec(`
        UPDATE chapters SET
            downloaded    = 0,
            downloaded_at = NULL,
            raw_path      = NULL
        WHERE manga_slug = ? AND source_id = ? AND number = ?
    `, chapter.MangaSlug, chapter.SourceID, chapter.Number)
	return err
}

func (db *DB) MarkUnoptimized(chapter models.Chapter) error {
	_, err := db.sql.Exec(`
        UPDATE chapters SET
            optimized      = 0,
            optimized_at   = NULL,
            optimized_path = NULL
        WHERE manga_slug = ? AND source_id = ? AND number = ?
    `, chapter.MangaSlug, chapter.SourceID, chapter.Number)
	return err
}

func (db *DB) MarkUnpacked(chapter models.Chapter) error {
	_, err := db.sql.Exec(`
        UPDATE chapters SET
            packed    = 0,
            packed_at = NULL,
            cbz_path  = NULL
        WHERE manga_slug = ? AND source_id = ? AND number = ?
    `, chapter.MangaSlug, chapter.SourceID, chapter.Number)
	return err
}

// --- Favorites ---

func (db *DB) AddFavorite(userID int, mangaSlug, sourceID string) error {
	_, err := db.sql.Exec(`
        INSERT OR IGNORE INTO favorites (user_id, manga_slug, source_id, added_at)
        VALUES (?, ?, ?, ?)
    `, userID, mangaSlug, sourceID, time.Now().UTC())
	return err
}

func (db *DB) RemoveFavorite(userID int, mangaSlug, sourceID string) error {
	_, err := db.sql.Exec(`
        DELETE FROM favorites WHERE user_id = ? AND manga_slug = ? AND source_id = ?
    `, userID, mangaSlug, sourceID)
	return err
}

func (db *DB) IsFavorite(userID int, mangaSlug, sourceID string) (bool, error) {
	var count int
	err := db.sql.QueryRow(`
        SELECT COUNT(*) FROM favorites
        WHERE user_id = ? AND manga_slug = ? AND source_id = ?
    `, userID, mangaSlug, sourceID).Scan(&count)
	return count > 0, err
}

// --- Read marks ---

func (db *DB) MarkRead(userID int, mangaSlug, sourceID string, number float64) error {
	_, err := db.sql.Exec(`
        INSERT OR IGNORE INTO read_marks (user_id, manga_slug, source_id, number, read_at)
        VALUES (?, ?, ?, ?, ?)
    `, userID, mangaSlug, sourceID, number, time.Now().UTC())
	return err
}

func (db *DB) MarkUnread(userID int, mangaSlug, sourceID string, number float64) error {
	_, err := db.sql.Exec(`
        DELETE FROM read_marks
        WHERE user_id = ? AND manga_slug = ? AND source_id = ? AND number = ?
    `, userID, mangaSlug, sourceID, number)
	return err
}

func (db *DB) ReadMarks(userID int, mangaSlug, sourceID string) ([]float64, error) {
	rows, err := db.sql.Query(`
        SELECT number FROM read_marks
        WHERE user_id = ? AND manga_slug = ? AND source_id = ?
    `, userID, mangaSlug, sourceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var numbers []float64
	for rows.Next() {
		var n float64
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		numbers = append(numbers, n)
	}
	return numbers, nil
}

// --- Users ---

func (db *DB) CreateUser(email, passwordHash string) (*models.User, error) {
	result, err := db.sql.Exec(`
        INSERT INTO users (email, password_hash, created_at)
        VALUES (?, ?, ?)
    `, email, passwordHash, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	id, _ := result.LastInsertId()
	return db.UserByID(int(id))
}

func (db *DB) UserByEmail(email string) (*models.User, error) {
	row := db.sql.QueryRow(`
        SELECT id, email, password_hash, created_at FROM users WHERE email = ?
    `, email)
	return scanUser(row)
}

func (db *DB) UserByID(id int) (*models.User, error) {
	row := db.sql.QueryRow(`
        SELECT id, email, password_hash, created_at FROM users WHERE id = ?
    `, id)
	return scanUser(row)
}

// --- Scan helpers ---

func scanUser(row *sql.Row) (*models.User, error) {
	var u models.User
	err := row.Scan(&u.ID, &u.Email, &u.PasswordHash, &u.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &u, err
}

func scanChapter(row *sql.Row) (*models.ChapterRecord, error) {
	var ch models.ChapterRecord
	var (
		rawPath, optimizedPath, cbzPath     sql.NullString
		releasedAt                          sql.NullTime
		downloadedAt, optimizedAt, packedAt sql.NullTime
	)
	err := row.Scan(
		&ch.MangaSlug, &ch.SourceID, &ch.Number, &ch.Title, &ch.URL, &releasedAt,
		&rawPath, &optimizedPath, &cbzPath,
		&ch.Downloaded, &downloadedAt,
		&ch.Optimized, &optimizedAt,
		&ch.Packed, &packedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	ch.RawPath = rawPath.String
	ch.OptimizedPath = optimizedPath.String
	ch.CBZPath = cbzPath.String

	if releasedAt.Valid {
		ch.ReleasedAt = releasedAt.Time.UTC()
	}
	if downloadedAt.Valid {
		ch.DownloadedAt = downloadedAt.Time.UTC()
	}
	if optimizedAt.Valid {
		ch.OptimizedAt = optimizedAt.Time.UTC()
	}
	if packedAt.Valid {
		ch.PackedAt = packedAt.Time.UTC()
	}
	return &ch, nil
}

func scanChapters(rows *sql.Rows) ([]models.ChapterRecord, error) {
	var chapters []models.ChapterRecord
	for rows.Next() {
		var ch models.ChapterRecord
		var (
			rawPath, optimizedPath, cbzPath     sql.NullString
			releasedAt                          sql.NullTime
			downloadedAt, optimizedAt, packedAt sql.NullTime
		)
		err := rows.Scan(
			&ch.MangaSlug, &ch.SourceID, &ch.Number, &ch.Title, &ch.URL, &releasedAt,
			&rawPath, &optimizedPath, &cbzPath,
			&ch.Downloaded, &downloadedAt,
			&ch.Optimized, &optimizedAt,
			&ch.Packed, &packedAt,
		)
		if err != nil {
			return nil, err
		}

		ch.RawPath = rawPath.String
		ch.OptimizedPath = optimizedPath.String
		ch.CBZPath = cbzPath.String

		if releasedAt.Valid {
			ch.ReleasedAt = releasedAt.Time.UTC()
		}
		if downloadedAt.Valid {
			ch.DownloadedAt = downloadedAt.Time.UTC()
		}
		if optimizedAt.Valid {
			ch.OptimizedAt = optimizedAt.Time.UTC()
		}
		if packedAt.Valid {
			ch.PackedAt = packedAt.Time.UTC()
		}
		chapters = append(chapters, ch)
	}
	return chapters, nil
}

func scanMangas(rows *sql.Rows) ([]models.MangaRecord, error) {
	var mangas []models.MangaRecord
	for rows.Next() {
		var m models.MangaRecord
		var genres string
		err := rows.Scan(
			&m.Slug, &m.SourceID, &m.Title, &m.Description,
			&m.CoverURL, &m.CoverPath, &m.Status, &genres,
			&m.AddedAt, &m.UpdatedAt,
		)
		if err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(genres), &m.Genres)
		mangas = append(mangas, m)
	}
	return mangas, nil
}
