package s3api

import (
	"bufio"
	"context"
	"encoding/xml"
	"io"
	"net/http"
	"os"
	"strconv"
	"time"

	"gpix/pkg/disguise"
	"gpix/pkg/gpmc"
)

func (s *Server) headObject(w http.ResponseWriter, r *http.Request, key string) {
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()

	it, found, err := s.findItem(ctx, key)
	if err != nil {
		writeError(w, r, errInternalError)
		return
	}
	if !found {
		writeError(w, r, errNoSuchKey)
		return
	}
	setObjectHeaders(w, it)
	w.WriteHeader(http.StatusOK)
}

func (s *Server) getObject(w http.ResponseWriter, r *http.Request, key string) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()

	it, found, err := s.findItem(ctx, key)
	if err != nil {
		writeError(w, r, errInternalError)
		return
	}
	if !found {
		writeError(w, r, errNoSuchKey)
		return
	}

	origURL, _, err := s.gp.GetDownloadURL(ctx, key)
	if err != nil || origURL == "" {
		writeError(w, r, errInternalError)
		return
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, origURL, nil)
	if err != nil {
		writeError(w, r, errInternalError)
		return
	}
	resp, err := s.gp.HTTPClient().Do(req)
	if err != nil {
		writeError(w, r, errInternalError)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		writeError(w, r, errInternalError)
		return
	}

	setObjectHeaders(w, it)

	br := bufio.NewReaderSize(resp.Body, 64*1024)
	head, _ := br.Peek(8192)
	if disguise.LooksDisguised(head) {
		if hdr, payload, exErr := disguise.Extract(br); exErr == nil {
			if hdr.Filename != "" {
				w.Header().Set("X-Amz-Meta-Filename", hdr.Filename)
			}
			w.Header().Set("Content-Length", strconv.FormatInt(hdr.PayloadSize, 10))
			w.WriteHeader(http.StatusOK)
			_, _ = io.Copy(w, payload)
			return
		}
	}

	if cl := resp.Header.Get("Content-Length"); cl != "" {
		w.Header().Set("Content-Length", cl)
	}
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, br)
}

func (s *Server) putObject(w http.ResponseWriter, r *http.Request, key string) {
	if r.ContentLength < 0 {
		writeError(w, r, errMissingContentLength)
		return
	}
	tmp, err := os.CreateTemp(s.cfg.TempDir, "s3put-*")
	if err != nil {
		writeError(w, r, errInternalError)
		return
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()

	if _, err := io.Copy(tmp, r.Body); err != nil {
		writeError(w, r, errInternalError)
		return
	}
	if err := tmp.Close(); err != nil {
		writeError(w, r, errInternalError)
		return
	}

	name := r.Header.Get("X-Amz-Meta-Filename")
	res, err := s.gp.UploadFile(r.Context(), tmp.Name(), gpmc.UploadOpts{
		OverrideName:      name,
		EncryptPassphrase: s.cfg.EncPassphrase,
	})
	if err != nil {
		writeError(w, r, errInternalError)
		return
	}

	w.Header().Set("ETag", `"`+res.MediaKey+`"`)
	w.WriteHeader(http.StatusOK)
}

func (s *Server) deleteObject(w http.ResponseWriter, r *http.Request, key string) {
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()

	results, err := s.gp.DeleteByMediaKeys(ctx, []string{key}, false)
	if err != nil {
		writeError(w, r, errInternalError)
		return
	}
	if e := results[key]; e != nil {
		writeError(w, r, errNoSuchKey)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) deleteObjects(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeError(w, r, errInternalError)
		return
	}
	var req deleteRequest
	if err := xml.Unmarshal(body, &req); err != nil {
		writeError(w, r, errMalformedXML)
		return
	}

	keys := make([]string, 0, len(req.Objects))
	for _, o := range req.Objects {
		keys = append(keys, o.Key)
	}

	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()

	results, err := s.gp.DeleteByMediaKeys(ctx, keys, false)
	if err != nil {
		writeError(w, r, errInternalError)
		return
	}

	res := deleteResult{XMLNS: s3XMLNS}
	for _, o := range req.Objects {
		if e := results[o.Key]; e != nil {
			res.Errors = append(res.Errors, deleteError{Key: o.Key, Code: "AccessDenied", Message: e.Error()})
			continue
		}
		if !req.Quiet {
			res.Deleted = append(res.Deleted, deletedObject{Key: o.Key})
		}
	}
	writeXML(w, http.StatusOK, res)
}

func setObjectHeaders(w http.ResponseWriter, it gpmc.MediaItem) {
	w.Header().Set("Content-Length", strconv.FormatInt(it.SizeBytes, 10))
	w.Header().Set("Last-Modified", httpDate(it.Mtime))
	w.Header().Set("ETag", etagFromSHA1(it.SHA1))
	w.Header().Set("Accept-Ranges", "none")
	if it.Filename != "" {
		w.Header().Set("X-Amz-Meta-Filename", it.Filename)
	}
}
