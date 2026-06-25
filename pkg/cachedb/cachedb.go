// Package cachedb is a SQLite-backed snapshot of the Google Photos library
// listing. It lets gpix serve S3 ListObjects / WebDAV PROPFIND (and any other
// "list everything" surface) instantly on startup from the last persisted
// snapshot, while the in-memory cache refreshes in the background. It implements
// library.Store.
//
// Uses the pure-Go modernc.org/sqlite driver (CGO-free). Run `go get
// modernc.org/sqlite` once.
package cachedb

import (
	"context"
	"database/sql"
	"time"

	"gpix/pkg/gpmc"

	_ "modernc.org/sqlite"
)

// Store persists library snapshots in SQLite.
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
	if err := s.init(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) init() error {
	const schema = `
CREATE TABLE IF NOT EXISTS media_items (
    media_key TEXT PRIMARY KEY,
    filename  TEXT,
    kind      INTEGER,
    size      INTEGER,
    mtime_ms  INTEGER,
    sha1      BLOB
);
CREATE TABLE IF NOT EXISTS library_meta (k TEXT PRIMARY KEY, v INTEGER);`
	_, err := s.db.Exec(schema)
	return err
}

// Load returns the persisted snapshot and the time it was saved.
func (s *Store) Load() ([]gpmc.MediaItem, time.Time, error) {
	rows, err := s.db.Query(`SELECT media_key, filename, kind, size, mtime_ms, sha1 FROM media_items`)
	if err != nil {
		return nil, time.Time{}, err
	}
	defer rows.Close()

	var items []gpmc.MediaItem
	for rows.Next() {
		var (
			it   gpmc.MediaItem
			kind int
			ms   int64
			sha1 []byte
		)
		if err := rows.Scan(&it.MediaKey, &it.Filename, &kind, &it.SizeBytes, &ms, &sha1); err != nil {
			return nil, time.Time{}, err
		}
		it.Kind = gpmc.MediaKind(kind)
		it.Mtime = time.UnixMilli(ms)
		it.SHA1 = sha1
		items = append(items, it)
	}
	if err := rows.Err(); err != nil {
		return nil, time.Time{}, err
	}

	var savedMs int64
	_ = s.db.QueryRow(`SELECT v FROM library_meta WHERE k = 'saved_at'`).Scan(&savedMs)
	var savedAt time.Time
	if savedMs > 0 {
		savedAt = time.UnixMilli(savedMs)
	}
	return items, savedAt, nil
}

// Save replaces the snapshot with items (run in a single transaction).
func (s *Store) Save(items []gpmc.MediaItem) error {
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `DELETE FROM media_items`); err != nil {
		return err
	}
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO media_items (media_key, filename, kind, size, mtime_ms, sha1) VALUES (?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, it := range items {
		if _, err := stmt.ExecContext(ctx, it.MediaKey, it.Filename, int(it.Kind), it.SizeBytes, it.Mtime.UnixMilli(), it.SHA1); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO library_meta (k, v) VALUES ('saved_at', ?)
		ON CONFLICT(k) DO UPDATE SET v = excluded.v`, time.Now().UnixMilli()); err != nil {
		return err
	}
	return tx.Commit()
}
