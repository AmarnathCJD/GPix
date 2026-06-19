package s3api

import (
	"crypto/md5"
	"crypto/rand"
	"encoding/hex"
	"io"
	"net/http"
	"os"
	"strconv"
	"time"

	"gpix/pkg/gpmc"
)

const maxUploadBytes = 5 << 30

type multipartUpload struct {
	key       string
	tmpPath   string
	parts     map[int]string
	totalSize int64
	created   time.Time
}

func (s *Server) initiateMultipart(w http.ResponseWriter, r *http.Request, key string) {
	id, err := newUploadID()
	if err != nil {
		writeError(w, r, errInternalError)
		return
	}
	tmp, err := os.CreateTemp(s.cfg.TempDir, "s3mp-*")
	if err != nil {
		writeError(w, r, errInternalError)
		return
	}
	path := tmp.Name()
	_ = tmp.Close()

	s.mpMu.Lock()
	s.uploads[id] = &multipartUpload{
		key:     key,
		tmpPath: path,
		parts:   make(map[int]string),
		created: time.Now(),
	}
	s.mpMu.Unlock()

	writeXML(w, http.StatusOK, initiateMultipartUploadResult{
		XMLNS:    s3XMLNS,
		Bucket:   s.cfg.Bucket,
		Key:      key,
		UploadId: id,
	})
}

func (s *Server) uploadPart(w http.ResponseWriter, r *http.Request, uploadID, partNumStr string) {
	partNum, err := strconv.Atoi(partNumStr)
	if err != nil || partNum < 1 {
		writeError(w, r, errInvalidRequest)
		return
	}

	s.mpMu.Lock()
	up := s.uploads[uploadID]
	s.mpMu.Unlock()
	if up == nil {
		writeError(w, r, errNoSuchUpload)
		return
	}

	// Parts must be uploaded in order: gpix has no real multipart object store,
	// so each part is appended to a single tempfile that becomes one upload.
	f, err := os.OpenFile(up.tmpPath, os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		writeError(w, r, errInternalError)
		return
	}
	defer f.Close()

	hasher := md5.New()
	limited := io.LimitReader(r.Body, maxUploadBytes-up.totalSize+1)
	n, err := io.Copy(io.MultiWriter(f, hasher), limited)
	if err != nil {
		writeError(w, r, errInternalError)
		return
	}
	if up.totalSize+n > maxUploadBytes {
		writeError(w, r, errEntityTooLarge)
		return
	}

	etag := `"` + hex.EncodeToString(hasher.Sum(nil)) + `"`
	s.mpMu.Lock()
	up.parts[partNum] = etag
	up.totalSize += n
	s.mpMu.Unlock()

	w.Header().Set("ETag", etag)
	w.WriteHeader(http.StatusOK)
}

func (s *Server) completeMultipart(w http.ResponseWriter, r *http.Request, key, uploadID string) {
	s.mpMu.Lock()
	up := s.uploads[uploadID]
	if up != nil {
		delete(s.uploads, uploadID)
	}
	s.mpMu.Unlock()
	if up == nil {
		writeError(w, r, errNoSuchUpload)
		return
	}
	defer os.Remove(up.tmpPath)

	res, err := s.gp.UploadFile(r.Context(), up.tmpPath, gpmc.UploadOpts{
		OverrideName:      key,
		EncryptPassphrase: s.cfg.EncPassphrase,
	})
	if err != nil {
		writeError(w, r, errInternalError)
		return
	}

	writeXML(w, http.StatusOK, completeMultipartUploadResult{
		XMLNS:  s3XMLNS,
		Bucket: s.cfg.Bucket,
		Key:    key,
		ETag:   `"` + res.MediaKey + `"`,
	})
}

func (s *Server) abortMultipart(w http.ResponseWriter, r *http.Request, key, uploadID string) {
	s.mpMu.Lock()
	up := s.uploads[uploadID]
	if up != nil {
		delete(s.uploads, uploadID)
	}
	s.mpMu.Unlock()
	if up == nil {
		writeError(w, r, errNoSuchUpload)
		return
	}
	_ = os.Remove(up.tmpPath)
	w.WriteHeader(http.StatusNoContent)
}

func newUploadID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
