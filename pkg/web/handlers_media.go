package web

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"mime"
	"net/http"
	"path/filepath"
	"strconv"
	"time"

	"gpix/pkg/disguise"
	"gpix/pkg/gpmc"
	"gpix/pkg/mediacrypt"
)

func (s *Server) handleThumb(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	if key == "" {
		http.NotFound(w, r)
		return
	}
	s.serveThumb(w, r, key, thumbSize(r))
}

func thumbSize(r *http.Request) int {
	size := 256
	if v := r.URL.Query().Get("size"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			switch n {
			case 64, 128, 256, 512:
				size = n
			}
		}
	}
	return size
}

func (s *Server) serveThumb(w http.ResponseWriter, r *http.Request, key string, size int) {
	// Encrypted/disguised photos have only a blank thumbnail on Google's side,
	// so generate one from the decrypted original (cached on disk).
	if s.lib != nil {
		if it, ok, _ := s.lib.Get(r.Context(), key); ok {
			display, class, disguised := classifyItem(it.Filename, it.Kind)
			if disguised && class == classPhoto && s.serveGeneratedThumb(w, r, key, display, size) {
				return
			}
		}
	}

	ctx, cancel := withTimeout(r.Context(), 20*time.Second)
	defer cancel()

	tok, err := s.gp.BearerToken(ctx)
	if err != nil {
		http.Error(w, "bearer: "+err.Error(), 500)
		return
	}
	url := gpmc.ThumbnailURL(key, size)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := s.gp.HTTPClient().Do(req)
	if err != nil {
		http.Error(w, err.Error(), 502)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		http.Error(w, fmt.Sprintf("thumb upstream %d", resp.StatusCode), resp.StatusCode)
		return
	}
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	w.Header().Set("Cache-Control", "private, max-age=86400, immutable")
	_, _ = io.Copy(w, resp.Body)
}

func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	key, err := s.verifyMedia(r.PathValue("token"))
	if err != nil {
		http.Error(w, "bad token", http.StatusForbidden)
		return
	}
	ctx, cancel := withTimeout(r.Context(), 30*time.Second)
	defer cancel()

	manifest, err := s.gp.GetStreamManifest(ctx, key, "hls")
	if err != nil {
		s.proxyDownload(w, r, key, false)
		return
	}

	levelQ := r.URL.Query().Get("level")
	if levelQ != "" {
		idx, perr := strconv.Atoi(levelQ)
		if perr == nil {
			variants := ParseMasterPlaylist(manifest)
			for _, v := range variants {
				if v.Index == idx {
					http.Redirect(w, r, v.PlaylistURL, http.StatusFound)
					return
				}
			}
		}
		http.Error(w, "unknown level", http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	w.Header().Set("Cache-Control", "private, max-age=300")
	_, _ = io.WriteString(w, manifest)
}

func (s *Server) handleRaw(w http.ResponseWriter, r *http.Request) {
	key, err := s.verifyMedia(r.PathValue("token"))
	if err != nil {
		http.Error(w, "bad token", http.StatusForbidden)
		return
	}
	// inline=1 renders in-browser (img/video) rather than downloading; either
	// way the bytes are un-disguised and decrypted as needed.
	attachment := r.URL.Query().Get("inline") != "1"
	s.proxyDownload(w, r, key, attachment)
}

func (s *Server) proxyDownload(w http.ResponseWriter, r *http.Request, mediaKey string, attachment bool) {
	ctx, cancel := withTimeout(r.Context(), 5*time.Minute)
	defer cancel()

	url, err := s.urlCache.Get(ctx, mediaKey)
	if err != nil {
		http.Error(w, "resolve: "+err.Error(), http.StatusBadGateway)
		return
	}
	resp, retried, err := s.doProxyGet(ctx, url, mediaKey, false)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	if (resp.StatusCode == 403 || resp.StatusCode == 410) && !retried {
		resp.Body.Close()
		s.urlCache.Invalidate(mediaKey)
		url, err = s.urlCache.Get(ctx, mediaKey)
		if err != nil {
			http.Error(w, "re-resolve: "+err.Error(), http.StatusBadGateway)
			return
		}
		resp, _, err = s.doProxyGet(ctx, url, mediaKey, true)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
	}

	br := bufio.NewReaderSize(resp.Body, 64*1024)
	// Always un-disguise / decrypt a disguised item, whether it's being
	// downloaded (attachment) or rendered inline (img/video). Only the
	// Content-Disposition differs.
	if resp.StatusCode == http.StatusOK {
		head, _ := br.Peek(8192)
		if disguise.LooksDisguised(head) {
			hdr, payload, err := disguise.Extract(br)
			if err == nil {
				// The unwrapped payload may itself be encrypted.
				if s.crypt != nil {
					pbr := bufio.NewReader(payload)
					if ph, _ := pbr.Peek(len(mediacrypt.Magic)); mediacrypt.HasMagic(ph) {
						eh, dr, derr := s.crypt.DecryptingReader(pbr)
						if derr != nil {
							http.Error(w, "decrypt: "+derr.Error(), http.StatusBadGateway)
							return
						}
						writeMediaHeaders(w, eh.Name, eh.OrigSize, attachment)
						_, _ = io.Copy(w, dr)
						dr.Close()
						return
					}
					writeMediaHeaders(w, hdr.Filename, hdr.PayloadSize, attachment)
					_, _ = io.Copy(w, pbr)
					return
				}
				writeMediaHeaders(w, hdr.Filename, hdr.PayloadSize, attachment)
				_, _ = io.Copy(w, payload)
				return
			}
		}
	}

	for _, h := range []string{"Content-Type", "Content-Length"} {
		if v := resp.Header.Get(h); v != "" {
			w.Header().Set(h, v)
		}
	}
	if attachment {
		if cd := resp.Header.Get("Content-Disposition"); cd != "" {
			w.Header().Set("Content-Disposition", cd)
		} else {
			w.Header().Set("Content-Disposition", "attachment")
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, br)
}

func writeMediaHeaders(w http.ResponseWriter, origName string, size int64, attachment bool) {
	if origName == "" {
		origName = "download.bin"
	}
	w.Header().Set("Content-Type", disguiseMIME(origName))
	if size >= 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	}
	dispType := "inline"
	if attachment {
		dispType = "attachment"
	}
	disp := mime.FormatMediaType(dispType, map[string]string{"filename": origName})
	if disp == "" {
		disp = dispType + `; filename="` + origName + `"`
	}
	w.Header().Set("Content-Disposition", disp)
	w.WriteHeader(http.StatusOK)
}

func disguiseMIME(filename string) string {
	if filename == "" {
		return "application/octet-stream"
	}
	if m := mime.TypeByExtension(filepath.Ext(filename)); m != "" {
		return m
	}
	return "application/octet-stream"
}

func (s *Server) doProxyGet(ctx context.Context, url, mediaKey string, _ bool) (*http.Response, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, false, err
	}
	resp, err := s.gp.HTTPClient().Do(req)
	return resp, false, err
}

func withTimeout(parent context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, d)
}
