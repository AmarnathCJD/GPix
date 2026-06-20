package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"gpix/pkg/gpmc"
	"gpix/pkg/gpmc/albumstore"
)

type albumDTO struct {
	MediaKey      string `json:"media_key"`
	Title         string `json:"title"`
	ItemCount     int    `json:"item_count"`
	CoverMediaKey string `json:"cover_media_key,omitempty"`
	Source        string `json:"source"`
}

func toAlbumDTO(a gpmc.Album) albumDTO {
	return albumDTO{
		MediaKey:      a.MediaKey,
		Title:         a.Title,
		ItemCount:     a.ItemCount,
		CoverMediaKey: a.CoverMediaKey,
		Source:        a.Source.String(),
	}
}

func (s *Server) handleAlbumsList(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	albums, err := s.gp.ListAlbums(ctx)
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, err.Error())
		return
	}
	out := make([]albumDTO, 0, len(albums))
	for _, a := range albums {
		out = append(out, toAlbumDTO(a))
	}
	writeJSON(w, http.StatusOK, map[string]any{"albums": out})
}

func (s *Server) handleAlbumCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Title     string   `json:"title"`
		MediaKeys []string `json:"media_keys"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid json")
		return
	}
	req.Title = strings.TrimSpace(req.Title)
	if req.Title == "" {
		writeJSONError(w, http.StatusBadRequest, "title is required")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()

	if _, ok, _ := s.gp.FindAlbumByTitle(ctx, req.Title); ok {
		writeJSONError(w, http.StatusConflict, "album already exists")
		return
	}

	key, err := s.gp.CreateAlbum(ctx, req.Title, req.MediaKeys)
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, err.Error())
		return
	}
	if store := s.gp.AlbumStore(); store != nil {
		_ = store.PutAlbum(ctx, albumstore.Album{MediaKey: key, Title: req.Title, CreatedAt: time.Now()})
		if len(req.MediaKeys) > 0 {
			_ = store.AddMembers(ctx, key, req.MediaKeys)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"album": albumDTO{MediaKey: key, Title: req.Title, ItemCount: len(req.MediaKeys), Source: gpmc.AlbumSourceUpstream.String()},
	})
}

func (s *Server) handleAlbumAdd(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AlbumMediaKey string   `json:"album_media_key"`
		AlbumTitle    string   `json:"album_title"`
		MediaKeys     []string `json:"media_keys"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid json")
		return
	}
	if len(req.MediaKeys) == 0 {
		writeJSONError(w, http.StatusBadRequest, "media_keys is required")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()

	albumKey := strings.TrimSpace(req.AlbumMediaKey)
	if albumKey == "" {
		if title := strings.TrimSpace(req.AlbumTitle); title != "" {
			album, ok, err := s.gp.FindAlbumByTitle(ctx, title)
			if err != nil {
				writeJSONError(w, http.StatusBadGateway, err.Error())
				return
			}
			if ok {
				albumKey = album.MediaKey
			} else {
				newKey, err := s.gp.CreateAlbum(ctx, title, req.MediaKeys)
				if err != nil {
					writeJSONError(w, http.StatusBadGateway, err.Error())
					return
				}
				if store := s.gp.AlbumStore(); store != nil {
					_ = store.PutAlbum(ctx, albumstore.Album{MediaKey: newKey, Title: title, CreatedAt: time.Now()})
					_ = store.AddMembers(ctx, newKey, req.MediaKeys)
				}
				writeJSON(w, http.StatusOK, map[string]any{"album_media_key": newKey, "added": len(req.MediaKeys)})
				return
			}
		}
	}
	if albumKey == "" {
		writeJSONError(w, http.StatusBadRequest, "album_media_key or album_title is required")
		return
	}

	if err := s.gp.AddMediaToAlbum(ctx, albumKey, req.MediaKeys); err != nil {
		writeJSONError(w, http.StatusBadGateway, err.Error())
		return
	}
	if store := s.gp.AlbumStore(); store != nil {
		_ = store.AddMembers(ctx, albumKey, req.MediaKeys)
	}
	writeJSON(w, http.StatusOK, map[string]any{"album_media_key": albumKey, "added": len(req.MediaKeys)})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeJSONError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"error": msg})
}

var errAlbumStoreMissing = errors.New("album store not configured")
