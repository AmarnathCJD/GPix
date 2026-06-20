package albumstore

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type entry struct {
	MediaKey      string   `json:"media_key"`
	Title         string   `json:"title"`
	AlbumID       string   `json:"album_id,omitempty"`
	CoverMediaKey string   `json:"cover_media_key,omitempty"`
	CreatedAt     int64    `json:"created_at"`
	Members       []string `json:"members,omitempty"`
}

type fileState struct {
	Version int     `json:"version"`
	Albums  []entry `json:"albums"`
}

type Store struct {
	path  string
	mu    sync.Mutex
	state fileState
}

func Open(path string) (*Store, error) {
	if path == "" {
		return nil, errors.New("albumstore: empty path")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	s := &Store{path: path}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) load() error {
	b, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			s.state = fileState{Version: 1}
			return nil
		}
		return err
	}
	if len(b) == 0 {
		s.state = fileState{Version: 1}
		return nil
	}
	return json.Unmarshal(b, &s.state)
}

func (s *Store) flushLocked() error {
	tmp := s.path + ".tmp"
	b, err := json.MarshalIndent(s.state, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func (s *Store) Close() error { return nil }

func (s *Store) Path() string { return s.path }

type Album struct {
	MediaKey      string
	Title         string
	AlbumID       string
	CoverMediaKey string
	CreatedAt     time.Time
}

func fromEntry(e entry) Album {
	return Album{
		MediaKey:      e.MediaKey,
		Title:         e.Title,
		AlbumID:       e.AlbumID,
		CoverMediaKey: e.CoverMediaKey,
		CreatedAt:     time.Unix(e.CreatedAt, 0),
	}
}

func (s *Store) PutAlbum(ctx context.Context, a Album) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, e := range s.state.Albums {
		if e.MediaKey == a.MediaKey {
			s.state.Albums[i].Title = a.Title
			if a.AlbumID != "" {
				s.state.Albums[i].AlbumID = a.AlbumID
			}
			if a.CoverMediaKey != "" {
				s.state.Albums[i].CoverMediaKey = a.CoverMediaKey
			}
			return s.flushLocked()
		}
	}
	created := a.CreatedAt
	if created.IsZero() {
		created = time.Now()
	}
	s.state.Albums = append(s.state.Albums, entry{
		MediaKey:      a.MediaKey,
		Title:         a.Title,
		AlbumID:       a.AlbumID,
		CoverMediaKey: a.CoverMediaKey,
		CreatedAt:     created.Unix(),
	})
	return s.flushLocked()
}

func (s *Store) GetAlbum(ctx context.Context, mediaKey string) (Album, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.state.Albums {
		if e.MediaKey == mediaKey {
			return fromEntry(e), nil
		}
	}
	return Album{}, nil
}

func (s *Store) GetAlbumByTitle(ctx context.Context, title string) (Album, bool, error) {
	target := strings.ToLower(strings.TrimSpace(title))
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.state.Albums {
		if strings.ToLower(e.Title) == target {
			return fromEntry(e), true, nil
		}
	}
	return Album{}, false, nil
}

func (s *Store) ListAlbums(ctx context.Context) ([]Album, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Album, 0, len(s.state.Albums))
	for _, e := range s.state.Albums {
		out = append(out, fromEntry(e))
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	return out, nil
}

func (s *Store) DeleteAlbum(ctx context.Context, mediaKey string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.state.Albums[:0]
	for _, e := range s.state.Albums {
		if e.MediaKey != mediaKey {
			out = append(out, e)
		}
	}
	s.state.Albums = out
	return s.flushLocked()
}

func (s *Store) AddMembers(ctx context.Context, mediaKey string, members []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, e := range s.state.Albums {
		if e.MediaKey != mediaKey {
			continue
		}
		existing := map[string]bool{}
		for _, m := range e.Members {
			existing[m] = true
		}
		for _, m := range members {
			if !existing[m] {
				s.state.Albums[i].Members = append(s.state.Albums[i].Members, m)
				existing[m] = true
			}
		}
		return s.flushLocked()
	}
	return errors.New("albumstore: unknown album")
}

func (s *Store) RemoveMembers(ctx context.Context, mediaKey string, members []string) error {
	rm := map[string]bool{}
	for _, m := range members {
		rm[m] = true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, e := range s.state.Albums {
		if e.MediaKey != mediaKey {
			continue
		}
		kept := e.Members[:0]
		for _, m := range e.Members {
			if !rm[m] {
				kept = append(kept, m)
			}
		}
		s.state.Albums[i].Members = kept
		return s.flushLocked()
	}
	return nil
}

func (s *Store) MembersOf(ctx context.Context, mediaKey string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.state.Albums {
		if e.MediaKey == mediaKey {
			out := make([]string, len(e.Members))
			copy(out, e.Members)
			return out, nil
		}
	}
	return nil, nil
}

func (s *Store) AlbumsOf(ctx context.Context, mediaKey string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, e := range s.state.Albums {
		for _, m := range e.Members {
			if m == mediaKey {
				out = append(out, e.MediaKey)
				break
			}
		}
	}
	return out, nil
}
