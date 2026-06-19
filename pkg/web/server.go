package web

import (
	"context"
	"crypto/rand"
	"embed"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"os"
	"time"

	"gpix/pkg/gpmc"
)

//go:embed templates/*.html templates/partials/*.html
var tmplFS embed.FS

//go:embed static
var staticFS embed.FS

type Server struct {
	cfg            Config
	gp             *gpmc.Client
	log            *slog.Logger
	httpSrv        *http.Server
	urlCache       *urlCache
	progressBus    *progressBus
	sessionSignKey []byte
	mediaSignKey   []byte
	tempSemaphore  chan struct{}
	pageTmpls      map[string]*template.Template
	extraMounts    map[string]http.Handler
}

func (s *Server) Mount(prefix string, h http.Handler) {
	if s.extraMounts == nil {
		s.extraMounts = make(map[string]http.Handler)
	}
	s.extraMounts[prefix] = h
}

func New(cfg Config, gp *gpmc.Client, log *slog.Logger) (*Server, error) {
	if log == nil {
		log = slog.Default()
	}
	if len(cfg.SecretKey) < 32 {
		return nil, errors.New("web: SecretKey must be at least 32 bytes")
	}
	s := &Server{
		cfg:            cfg,
		gp:             gp,
		log:            log,
		urlCache:       newURLCache(gp),
		progressBus:    newProgressBus(),
		sessionSignKey: deriveKey(cfg.SecretKey, "session"),
		mediaSignKey:   deriveKey(cfg.SecretKey, "media"),
		tempSemaphore:  make(chan struct{}, cfg.MaxConcurrentUploads),
	}
	if err := s.loadTemplates(); err != nil {
		return nil, fmt.Errorf("web: load templates: %w", err)
	}
	s.httpSrv = &http.Server{
		Addr:              cfg.Listen,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
	return s, nil
}

func (s *Server) Run(ctx context.Context) error {
	s.httpSrv.Handler = s.routes()
	errCh := make(chan error, 1)
	go func() {
		s.log.Info("web server listening", "addr", s.cfg.Listen)
		if err := s.httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
		close(errCh)
	}()
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return s.httpSrv.Shutdown(shutdownCtx)
	case err := <-errCh:
		return err
	}
}

func LoadOrCreateSecret(path string) ([]byte, error) {
	if data, err := os.ReadFile(path); err == nil {
		if len(data) < 32 {
			return nil, fmt.Errorf("web: secret.key at %s is shorter than 32 bytes", path)
		}
		return data, nil
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, b, 0600); err != nil {
		return nil, err
	}
	return b, nil
}
