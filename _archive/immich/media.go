package immich

import (
	"bufio"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"mime"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"gpix/pkg/disguise"
	"gpix/pkg/gpmc"
	"gpix/pkg/mediacrypt"
)

func errUpstream(status int) error { return fmt.Errorf("immich: upstream status %d", status) }

func isoTime(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

// displayName returns the user-facing filename (disguise ".mp4" stripped).
func displayName(it gpmc.MediaItem) string {
	if orig, ok := disguise.LooksLikeDisguisedFilename(it.Filename); ok {
		return orig
	}
	return it.Filename
}

func mimeFor(name string) string {
	if m := mime.TypeByExtension(filepath.Ext(name)); m != "" {
		if i := strings.IndexByte(m, ';'); i >= 0 {
			m = m[:i]
		}
		return m
	}
	return "application/octet-stream"
}

func (s *Server) assetFromItem(it gpmc.MediaItem) assetResponse {
	name := displayName(it)
	typ := "IMAGE"
	if it.Kind == gpmc.KindVideo {
		typ = "VIDEO"
	}
	t := isoTime(it.Mtime)
	return assetResponse{
		ID:               assetID(it.MediaKey),
		DeviceAssetID:    assetID(it.MediaKey),
		OwnerID:          s.userID(),
		DeviceID:         "gpix",
		Type:             typ,
		OriginalPath:     "/gpix/" + name,
		OriginalFileName: name,
		OriginalMimeType: mimeFor(name),
		Resized:          true,
		FileCreatedAt:    t,
		FileModifiedAt:   t,
		LocalDateTime:    t,
		UpdatedAt:        t,
		Duration:         "0:00:00.00000",
		Checksum:         base64.StdEncoding.EncodeToString(it.SHA1),
		HasMetadata:      true,
		People:           []any{},
	}
}

// thumbnailSize maps Immich's ?size=thumbnail|preview to a gpix thumbnail size.
func thumbnailSize(r *http.Request) int {
	switch r.URL.Query().Get("size") {
	case "preview", "fullsize":
		return 512
	default:
		return 256
	}
}

func (s *Server) proxyThumbnail(w http.ResponseWriter, r *http.Request, mediaKey string, size int) {
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	tok, err := s.gp.BearerToken(ctx)
	if err != nil {
		http.Error(w, "bearer", http.StatusBadGateway)
		return
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, gpmc.ThumbnailURL(mediaKey, size), nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := s.gp.HTTPClient().Do(req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		http.Error(w, "thumbnail upstream", resp.StatusCode)
		return
	}
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	w.Header().Set("Cache-Control", "private, max-age=86400")
	_, _ = io.Copy(w, resp.Body)
}

// --- original fetch (download + un-disguise + decrypt) ---

type readCloser struct {
	r io.Reader
	c io.Closer
}

func (rc readCloser) Read(p []byte) (int, error) { return rc.r.Read(p) }
func (rc readCloser) Close() error               { return rc.c.Close() }

type multiReadCloser struct {
	r       io.Reader
	closers []io.Closer
}

func (m multiReadCloser) Read(p []byte) (int, error) { return m.r.Read(p) }
func (m multiReadCloser) Close() error {
	var first error
	for _, c := range m.closers {
		if err := c.Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}

type originalMeta struct {
	ContentType string
	Size        int64
	Name        string
}

// fetchOriginal resolves and returns the decrypted, un-disguised original bytes
// for a media key. The caller must Close the returned reader.
func (s *Server) fetchOriginal(ctx context.Context, mediaKey string) (io.ReadCloser, originalMeta, error) {
	orig, _, err := s.gp.GetDownloadURL(ctx, mediaKey)
	if err != nil {
		return nil, originalMeta{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, orig, nil)
	if err != nil {
		return nil, originalMeta{}, err
	}
	resp, err := s.gp.HTTPClient().Do(req)
	if err != nil {
		return nil, originalMeta{}, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, originalMeta{}, errUpstream(resp.StatusCode)
	}

	meta := originalMeta{ContentType: resp.Header.Get("Content-Type"), Size: -1}
	if cl := resp.Header.Get("Content-Length"); cl != "" {
		if n, perr := strconv.ParseInt(cl, 10, 64); perr == nil {
			meta.Size = n
		}
	}

	br := bufio.NewReaderSize(resp.Body, 64*1024)
	head, _ := br.Peek(8192)
	if disguise.LooksDisguised(head) {
		hdr, payload, derr := disguise.Extract(br)
		if derr == nil {
			meta.Name = hdr.Filename
			meta.Size = hdr.PayloadSize
			meta.ContentType = mimeFor(hdr.Filename)
			if s.crypt != nil {
				pbr := bufio.NewReader(payload)
				if ph, _ := pbr.Peek(len(mediacrypt.Magic)); mediacrypt.HasMagic(ph) {
					eh, dr, eerr := s.crypt.DecryptingReader(pbr)
					if eerr != nil {
						resp.Body.Close()
						return nil, originalMeta{}, eerr
					}
					meta.Name = eh.Name
					meta.Size = eh.OrigSize
					meta.ContentType = mimeFor(eh.Name)
					return multiReadCloser{r: dr, closers: []io.Closer{dr, resp.Body}}, meta, nil
				}
				return readCloser{r: pbr, c: resp.Body}, meta, nil
			}
			return readCloser{r: payload, c: resp.Body}, meta, nil
		}
	}
	return readCloser{r: br, c: resp.Body}, meta, nil
}
