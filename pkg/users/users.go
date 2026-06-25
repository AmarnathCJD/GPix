// Package users persists the set of people allowed to sign into gpix via OIDC,
// and enforces the email allowlist + max-users registration cap. SQLite via the
// pure-Go modernc.org/sqlite driver. Run `go get modernc.org/sqlite` once.
package users

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// ErrNotFound is returned when a subject is not registered.
var ErrNotFound = errors.New("users: not found")

// User is a registered identity.
type User struct {
	Subject   string
	Email     string
	Name      string
	CreatedAt time.Time
}

// Store is a SQLite-backed user store.
type Store struct {
	db *sql.DB
}

// Open opens (creating if needed) the database at path.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if _, err := db.Exec(`
CREATE TABLE IF NOT EXISTS users (
    subject    TEXT PRIMARY KEY,
    email      TEXT,
    name       TEXT,
    created_at INTEGER NOT NULL
);`); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

// Get returns a user by OIDC subject.
func (s *Store) Get(ctx context.Context, subject string) (User, error) {
	var (
		u  User
		ts int64
	)
	err := s.db.QueryRowContext(ctx, `SELECT subject, email, name, created_at FROM users WHERE subject = ?`, subject).
		Scan(&u.Subject, &u.Email, &u.Name, &ts)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, err
	}
	u.CreatedAt = time.Unix(ts, 0)
	return u, nil
}

// Count returns the number of registered users.
func (s *Store) Count(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&n)
	return n, err
}

// Create inserts (or updates) a user.
func (s *Store) Create(ctx context.Context, u User) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO users (subject, email, name, created_at) VALUES (?, ?, ?, ?)
ON CONFLICT(subject) DO UPDATE SET email = excluded.email, name = excluded.name`,
		u.Subject, u.Email, u.Name, time.Now().Unix())
	return err
}

// List returns all users, newest first.
func (s *Store) List(ctx context.Context) ([]User, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT subject, email, name, created_at FROM users ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		var (
			u  User
			ts int64
		)
		if err := rows.Scan(&u.Subject, &u.Email, &u.Name, &ts); err != nil {
			return nil, err
		}
		u.CreatedAt = time.Unix(ts, 0)
		out = append(out, u)
	}
	return out, rows.Err()
}

// Allowlisted reports whether email is permitted by the allowlist. An empty
// list allows everyone. Entries may be exact emails ("a@b.com") or domains
// ("@b.com" or "b.com"); matching is case-insensitive.
func Allowlisted(email string, allowlist []string) bool {
	if len(allowlist) == 0 {
		return true
	}
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return false
	}
	domain := ""
	if i := strings.LastIndexByte(email, '@'); i >= 0 {
		domain = email[i+1:]
	}
	for _, entry := range allowlist {
		entry = strings.ToLower(strings.TrimSpace(entry))
		if entry == "" {
			continue
		}
		if strings.HasPrefix(entry, "@") {
			if domain == entry[1:] {
				return true
			}
			continue
		}
		if !strings.Contains(entry, "@") { // bare domain
			if domain == entry {
				return true
			}
			continue
		}
		if email == entry {
			return true
		}
	}
	return false
}
