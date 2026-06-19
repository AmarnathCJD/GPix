package s3api

import (
	"context"
	"encoding/hex"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"gpix/pkg/gpmc"
)

type Server struct {
	cfg Config
	gp  *gpmc.Client
	log *slog.Logger

	mpMu    sync.Mutex
	uploads map[string]*multipartUpload
}

func NewServer(cfg Config, gp *gpmc.Client, log *slog.Logger) (*Server, error) {
	if err := cfg.normalize(); err != nil {
		return nil, err
	}
	if log == nil {
		log = slog.Default()
	}
	return &Server{
		cfg:     cfg,
		gp:      gp,
		log:     log,
		uploads: make(map[string]*multipartUpload),
	}, nil
}

func (s *Server) Handler() http.Handler {
	return http.HandlerFunc(s.route)
}

func (s *Server) route(w http.ResponseWriter, r *http.Request) {
	if authErr := s.verifySigV4(r); authErr != (apiError{}) {
		writeError(w, r, authErr)
		return
	}

	bucket, key := splitPath(r.URL.Path)
	q := r.URL.Query()

	switch {
	case bucket == "":
		if r.Method == http.MethodGet {
			s.listBuckets(w, r)
			return
		}
		writeError(w, r, errMethodNotAllowed)
		return

	case bucket != s.cfg.Bucket:
		writeError(w, r, errNoSuchBucket)
		return

	case key == "":
		s.routeBucket(w, r, q)
		return

	default:
		s.routeObject(w, r, key, q)
	}
}

func (s *Server) routeBucket(w http.ResponseWriter, r *http.Request, q map[string][]string) {
	switch r.Method {
	case http.MethodGet:
		s.listObjectsV2(w, r)
	case http.MethodPost:
		if _, ok := q["delete"]; ok {
			s.deleteObjects(w, r)
			return
		}
		writeError(w, r, errInvalidRequest)
	case http.MethodHead:
		w.WriteHeader(http.StatusOK)
	default:
		writeError(w, r, errMethodNotAllowed)
	}
}

func (s *Server) routeObject(w http.ResponseWriter, r *http.Request, key string, q map[string][]string) {
	_, hasUploads := q["uploads"]
	uploadID := first(q["uploadId"])

	switch r.Method {
	case http.MethodGet:
		s.getObject(w, r, key)
	case http.MethodHead:
		s.headObject(w, r, key)
	case http.MethodDelete:
		if uploadID != "" {
			s.abortMultipart(w, r, key, uploadID)
			return
		}
		s.deleteObject(w, r, key)
	case http.MethodPost:
		if hasUploads {
			s.initiateMultipart(w, r, key)
			return
		}
		if uploadID != "" {
			s.completeMultipart(w, r, key, uploadID)
			return
		}
		writeError(w, r, errInvalidRequest)
	case http.MethodPut:
		if uploadID != "" {
			s.uploadPart(w, r, uploadID, first(q["partNumber"]))
			return
		}
		s.putObject(w, r, key)
	default:
		writeError(w, r, errMethodNotAllowed)
	}
}

func splitPath(p string) (bucket, key string) {
	p = strings.TrimPrefix(p, "/")
	if p == "" {
		return "", ""
	}
	bucket, key, _ = strings.Cut(p, "/")
	return bucket, key
}

func first(v []string) string {
	if len(v) == 0 {
		return ""
	}
	return v[0]
}

func etagFromSHA1(sha1 []byte) string {
	if len(sha1) == 0 {
		return `"00000000000000000000000000000000"`
	}
	return `"` + hex.EncodeToString(sha1) + `"`
}

func httpDate(t time.Time) string {
	return t.UTC().Format(http.TimeFormat)
}

func isoDate(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000Z")
}

// findItem scans library pages for an object by MediaKey. The Google Photos
// protocol has no point-lookup by media_key, so a linear scan is the only path;
// DeleteByMediaKeys in gpmc does the same.
func (s *Server) findItem(ctx context.Context, key string) (gpmc.MediaItem, bool, error) {
	cursor := ""
	for {
		page, err := s.gp.ListPage(ctx, cursor)
		if err != nil {
			return gpmc.MediaItem{}, false, err
		}
		for _, it := range page.Items {
			if it.MediaKey == key {
				return it, true, nil
			}
		}
		if page.NextToken == "" {
			return gpmc.MediaItem{}, false, nil
		}
		cursor = page.NextToken
	}
}
