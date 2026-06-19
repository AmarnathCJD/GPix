package webdav

import (
	"log/slog"
	"net/http"

	"golang.org/x/net/webdav"

	"gpix/pkg/gpmc"
)

const Prefix = "/dav"

type Server struct {
	cfg     Config
	gp      *gpmc.Client
	log     *slog.Logger
	handler *webdav.Handler
}

func NewServer(cfg Config, gp *gpmc.Client, log *slog.Logger) (*Server, error) {
	if log == nil {
		log = slog.Default()
	}
	s := &Server{cfg: cfg, gp: gp, log: log}
	s.handler = &webdav.Handler{
		Prefix:     Prefix,
		FileSystem: newFS(gp, cfg),
		LockSystem: webdav.NewMemLS(),
		Logger: func(r *http.Request, err error) {
			if err != nil {
				log.Debug("webdav", "method", r.Method, "path", r.URL.Path, "err", err)
			}
		},
	}
	return s, nil
}

func (s *Server) Handler() http.Handler {
	return s.basicAuth(http.HandlerFunc(s.serve))
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodOptions:
		w.Header().Set("Allow", "OPTIONS, GET, HEAD, PUT, DELETE, MKCOL, PROPFIND, LOCK, UNLOCK")
		w.Header().Set("DAV", "1, 2")
		w.Header().Set("MS-Author-Via", "DAV")
		w.WriteHeader(http.StatusOK)
		return
	case "MKCOL":
		// Collections are virtual; the x/net handler can only surface a 405
		// for a rejected Mkdir, so answer 403 here per the gpix contract.
		http.Error(w, "collections are read-only", http.StatusForbidden)
		return
	}
	s.handler.ServeHTTP(w, r)
}
