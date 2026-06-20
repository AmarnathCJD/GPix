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
	"path/filepath"
	"sync"
	"time"

	"gpix/pkg/gpmc"
	"gpix/pkg/gwcreds"
	"gpix/pkg/library"
	"gpix/pkg/mediacrypt"
	"gpix/pkg/share"
)

//go:embed templates/*.html templates/partials/*.html
var tmplFS embed.FS

//go:embed static
var staticFS embed.FS

type Server struct {
	cfg            Config
	gp             *gpmc.Client
	lib            *library.Cache
	gw             *gwcreds.Store
	crypt          *mediacrypt.Manager
	share          *share.Store
	log            *slog.Logger
	httpSrv        *http.Server
	urlCache       *urlCache
	progressBus    *progressBus
	sessionSignKey []byte
	mediaSignKey   []byte
	tempSemaphore  chan struct{}
	pageTmpls      map[string]*template.Template
	thumbDir       string
	thumbLocks     sync.Map
}

// New builds the web server. lib is the shared library cache, gw the gateway-
// credentials store, crypt the media-encryption manager, and sh the share store
// backing the public /s/ links. Any of them may be nil to disable the related
// feature.
func New(cfg Config, gp *gpmc.Client, lib *library.Cache, gw *gwcreds.Store, crypt *mediacrypt.Manager, sh *share.Store, log *slog.Logger) (*Server, error) {
	if log == nil {
		log = slog.Default()
	}
	if len(cfg.SecretKey) < 32 {
		return nil, errors.New("web: SecretKey must be at least 32 bytes")
	}

	thumbBase := cfg.DataDir
	if thumbBase == "" {
		thumbBase = cfg.TempDir
	}
	thumbDir := filepath.Join(thumbBase, "gpix-thumbcache")
	_ = os.MkdirAll(thumbDir, 0o700)

	s := &Server{
		cfg:            cfg,
		gp:             gp,
		lib:            lib,
		gw:             gw,
		crypt:          crypt,
		share:          sh,
		log:            log,
		urlCache:       newURLCache(gp),
		progressBus:    newProgressBus(),
		sessionSignKey: deriveKey(cfg.SecretKey, "session"),
		mediaSignKey:   deriveKey(cfg.SecretKey, "media"),
		tempSemaphore:  make(chan struct{}, cfg.MaxConcurrentUploads),
		thumbDir:       thumbDir,
	}
	if err := s.loadTemplates(); err != nil {
		return nil, fmt.Errorf("web: load templates: %w", err)
	}
	s.httpSrv = &http.Server{
		Addr:              cfg.Listen,
		Handler:           s.routes(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
	return s, nil
}

func (s *Server) Run(ctx context.Context) error {
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
