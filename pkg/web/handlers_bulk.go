package web

import (
	"archive/zip"
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"gpix/pkg/disguise"
)

type bulkDeleteRequest struct {
	Keys      []string `json:"keys"`
	Permanent bool     `json:"permanent"`
}

type bulkDeleteResponse struct {
	Deleted int               `json:"deleted"`
	Errors  map[string]string `json:"errors"`
}

func (s *Server) handleBulkDelete(w http.ResponseWriter, r *http.Request) {
	var req bulkDeleteRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		http.Error(w, "bad request body", http.StatusBadRequest)
		return
	}
	keys := dedupeNonEmpty(req.Keys)
	if len(keys) == 0 {
		http.Error(w, "no keys", http.StatusBadRequest)
		return
	}

	ctx, cancel := withTimeout(r.Context(), 2*time.Minute)
	defer cancel()

	results, err := s.gp.DeleteByMediaKeys(ctx, keys, req.Permanent)
	if err != nil {
		s.log.Error("bulk delete", "count", len(keys), "err", err)
		http.Error(w, "delete failed: "+err.Error(), http.StatusBadGateway)
		return
	}

	resp := bulkDeleteResponse{Errors: map[string]string{}}
	for _, k := range keys {
		if e := results[k]; e != nil {
			resp.Errors[k] = e.Error()
		} else {
			resp.Deleted++
		}
	}
	s.log.Info("bulk deleted", "deleted", resp.Deleted, "errors", len(resp.Errors),
		"permanent", req.Permanent, "user", userFromCtx(r.Context()))

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(resp)
}

func (s *Server) handleBulkDownload(w http.ResponseWriter, r *http.Request) {
	keys := dedupeNonEmpty(strings.Split(r.URL.Query().Get("keys"), ","))
	if len(keys) == 0 {
		http.Error(w, "no keys", http.StatusBadRequest)
		return
	}

	ctx, cancel := withTimeout(r.Context(), 30*time.Minute)
	defer cancel()

	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition",
		fmt.Sprintf(`attachment; filename="gpix-%d-items.zip"`, len(keys)))

	zw := zip.NewWriter(w)
	defer zw.Close()

	used := make(map[string]int, len(keys))
	for _, key := range keys {
		if err := ctx.Err(); err != nil {
			return
		}
		name, err := s.zipOneEntry(ctx, zw, key, used)
		if err != nil {
			// Header may already be flushed mid-stream; we cannot signal a clean
			// HTTP error, so log and keep going with remaining entries.
			s.log.Error("bulk zip entry", "key", key, "name", name, "err", err)
		}
	}
}

func (s *Server) zipOneEntry(ctx context.Context, zw *zip.Writer, key string, used map[string]int) (string, error) {
	url, err := s.urlCache.Get(ctx, key)
	if err != nil {
		return key, fmt.Errorf("resolve: %w", err)
	}
	resp, _, err := s.doProxyGet(ctx, url, key, false)
	if err != nil {
		return key, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return key, fmt.Errorf("upstream %d", resp.StatusCode)
	}

	br := bufio.NewReaderSize(resp.Body, 64*1024)
	name := key
	body := io.Reader(br)

	head, _ := br.Peek(8192)
	if disguise.LooksDisguised(head) {
		if hdr, payload, eErr := disguise.Extract(br); eErr == nil {
			if hdr.Filename != "" {
				name = hdr.Filename
			}
			body = payload
		}
	}

	entry, err := zw.Create(uniqueZipName(name, used))
	if err != nil {
		return name, err
	}
	buf := make([]byte, 64*1024)
	if _, err := io.CopyBuffer(entry, body, buf); err != nil {
		return name, err
	}
	return name, nil
}

func dedupeNonEmpty(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, v := range in {
		v = strings.TrimSpace(v)
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}

func uniqueZipName(name string, used map[string]int) string {
	if name == "" {
		name = "file"
	}
	if used[name] == 0 {
		used[name] = 1
		return name
	}
	n := used[name]
	used[name] = n + 1
	dot := strings.LastIndexByte(name, '.')
	if dot <= 0 {
		return fmt.Sprintf("%s (%d)", name, n)
	}
	return fmt.Sprintf("%s (%d)%s", name[:dot], n, name[dot:])
}
