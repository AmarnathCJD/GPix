// Package share persists password-protected, expiring share links in SQLite.
// A share points at one OR MORE Google Photos media keys; the public endpoints
// in pkg/web resolve, decrypt (if needed) and serve them without exposing the
// user's session or encryption key.
//
// SQLite is provided by the pure-Go modernc.org/sqlite driver, so the static
// (CGO-free) build keeps working. Run `go get modernc.org/sqlite` once.
package share

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"time"

	"golang.org/x/crypto/bcrypt"

	_ "modernc.org/sqlite"
)

// ErrNotFound is returned when a token does not exist.
var ErrNotFound = errors.New("share: not found")

// ShareItem is one media item within a share.
type ShareItem struct {
	MediaKey string
	FileName string
	IsVideo  bool
}

// Share is a single share link, holding one or more items.
type Share struct {
	Token         string
	Title         string
	HasPassword   bool
	ExpiresAt     time.Time // zero = never
	MaxDownloads  int64     // 0 = unlimited
	Downloads     int64
	AllowOriginal bool
	CreatedAt     time.Time
	Items         []ShareItem

	passwordHash string
}

// CreateParams describes a new share.
type CreateParams struct {
	Title         string
	Items         []ShareItem
	Password      string        // empty = no password
	TTL           time.Duration // 0 = never expires
	MaxDownloads  int64         // 0 = unlimited
	AllowOriginal bool
}

// Store is a SQLite-backed share store.
type Store struct {
	db *sql.DB
}

// Open opens (creating if needed) the SQLite database at path.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if err := s.init(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) init(ctx context.Context) error {
	const schema = `
CREATE TABLE IF NOT EXISTS shares (
    token          TEXT PRIMARY KEY,
    title          TEXT,
    password_hash  TEXT,
    expires_at     INTEGER,
    max_downloads  INTEGER NOT NULL DEFAULT 0,
    downloads      INTEGER NOT NULL DEFAULT 0,
    allow_original INTEGER NOT NULL DEFAULT 1,
    created_at     INTEGER NOT NULL,
    media_key      TEXT NOT NULL DEFAULT '',
    file_name      TEXT NOT NULL DEFAULT '',
    is_video       INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS share_items (
    token     TEXT NOT NULL,
    idx       INTEGER NOT NULL,
    media_key TEXT NOT NULL,
    file_name TEXT,
    is_video  INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (token, idx)
);`
	_, err := s.db.ExecContext(ctx, schema)
	if err != nil {
		return err
	}
	// Best-effort migration for databases created before the title column.
	_, _ = s.db.ExecContext(ctx, `ALTER TABLE shares ADD COLUMN title TEXT`)
	return nil
}

func newToken() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// Create inserts a new share with its items and returns it.
func (s *Store) Create(ctx context.Context, p CreateParams) (Share, error) {
	if len(p.Items) == 0 {
		return Share{}, errors.New("share: no items")
	}
	token, err := newToken()
	if err != nil {
		return Share{}, err
	}
	var pwHash sql.NullString
	if p.Password != "" {
		h, err := bcrypt.GenerateFromPassword([]byte(p.Password), 12)
		if err != nil {
			return Share{}, err
		}
		pwHash = sql.NullString{String: string(h), Valid: true}
	}
	var expires sql.NullInt64
	if p.TTL > 0 {
		expires = sql.NullInt64{Int64: time.Now().Add(p.TTL).Unix(), Valid: true}
	}
	now := time.Now()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Share{}, err
	}
	defer tx.Rollback()

	first := p.Items[0]
	if _, err := tx.ExecContext(ctx, `
INSERT INTO shares (token, title, password_hash, expires_at, max_downloads, downloads, allow_original, created_at, media_key, file_name, is_video)
VALUES (?, ?, ?, ?, ?, 0, ?, ?, ?, ?, ?)`,
		token, p.Title, pwHash, expires, p.MaxDownloads, b2i(p.AllowOriginal), now.Unix(),
		first.MediaKey, first.FileName, b2i(first.IsVideo)); err != nil {
		return Share{}, err
	}
	for i, it := range p.Items {
		if _, err := tx.ExecContext(ctx, `
INSERT INTO share_items (token, idx, media_key, file_name, is_video) VALUES (?, ?, ?, ?, ?)`,
			token, i, it.MediaKey, it.FileName, b2i(it.IsVideo)); err != nil {
			return Share{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return Share{}, err
	}
	return s.Get(ctx, token)
}

// Get returns a single share by token, with its items.
func (s *Store) Get(ctx context.Context, token string) (Share, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT token, title, password_hash, expires_at, max_downloads, downloads, allow_original, created_at, media_key, file_name, is_video
FROM shares WHERE token = ?`, token)
	sh, legacy, err := scanShare(row)
	if err != nil {
		return Share{}, err
	}
	items, err := s.items(ctx, token)
	if err != nil {
		return Share{}, err
	}
	if len(items) == 0 && legacy.MediaKey != "" { // pre-multi-item share
		items = []ShareItem{legacy}
	}
	sh.Items = items
	return sh, nil
}

func (s *Store) items(ctx context.Context, token string) ([]ShareItem, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT media_key, file_name, is_video FROM share_items WHERE token = ? ORDER BY idx`, token)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ShareItem
	for rows.Next() {
		var (
			it      ShareItem
			isVideo int64
			fname   sql.NullString
		)
		if err := rows.Scan(&it.MediaKey, &fname, &isVideo); err != nil {
			return nil, err
		}
		it.FileName = fname.String
		it.IsVideo = isVideo != 0
		out = append(out, it)
	}
	return out, rows.Err()
}

// List returns all shares (without item detail), newest first.
func (s *Store) List(ctx context.Context) ([]Share, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT token, title, password_hash, expires_at, max_downloads, downloads, allow_original, created_at, media_key, file_name, is_video
FROM shares ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Share
	for rows.Next() {
		sh, _, err := scanShare(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sh)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		items, err := s.items(ctx, out[i].Token)
		if err != nil {
			return nil, err
		}
		out[i].Items = items
	}
	return out, nil
}

// Delete removes a share and its items.
func (s *Store) Delete(ctx context.Context, token string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM share_items WHERE token = ?`, token); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM shares WHERE token = ?`, token); err != nil {
		return err
	}
	return tx.Commit()
}

// RecordDownload increments the share's download counter.
func (s *Store) RecordDownload(ctx context.Context, token string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE shares SET downloads = downloads + 1 WHERE token = ?`, token)
	return err
}

// VerifyPassword reports whether pw matches; a share with no password is open.
func (sh Share) VerifyPassword(pw string) bool {
	if !sh.HasPassword {
		return true
	}
	return bcrypt.CompareHashAndPassword([]byte(sh.passwordHash), []byte(pw)) == nil
}

// Active reports whether the share can still be served at time now.
func (sh Share) Active(now time.Time) bool {
	if !sh.ExpiresAt.IsZero() && now.After(sh.ExpiresAt) {
		return false
	}
	if sh.MaxDownloads > 0 && sh.Downloads >= sh.MaxDownloads {
		return false
	}
	return true
}

type rowScanner interface {
	Scan(dest ...any) error
}

// scanShare reads a shares row, returning the Share and the legacy single-item
// fields (for pre-multi-item rows).
func scanShare(sc rowScanner) (Share, ShareItem, error) {
	var (
		sh       Share
		title    sql.NullString
		pwHash   sql.NullString
		expires  sql.NullInt64
		allowOrg int64
		created  int64
		legacy   ShareItem
		legVideo int64
		legName  sql.NullString
	)
	err := sc.Scan(&sh.Token, &title, &pwHash, &expires, &sh.MaxDownloads, &sh.Downloads, &allowOrg, &created,
		&legacy.MediaKey, &legName, &legVideo)
	if errors.Is(err, sql.ErrNoRows) {
		return Share{}, ShareItem{}, ErrNotFound
	}
	if err != nil {
		return Share{}, ShareItem{}, err
	}
	sh.Title = title.String
	sh.AllowOriginal = allowOrg != 0
	sh.HasPassword = pwHash.Valid && pwHash.String != ""
	sh.passwordHash = pwHash.String
	if expires.Valid {
		sh.ExpiresAt = time.Unix(expires.Int64, 0)
	}
	sh.CreatedAt = time.Unix(created, 0)
	legacy.FileName = legName.String
	legacy.IsVideo = legVideo != 0
	return sh, legacy, nil
}

func b2i(b bool) int64 {
	if b {
		return 1
	}
	return 0
}
