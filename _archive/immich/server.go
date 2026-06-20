// Package immich exposes a subset of the Immich REST API backed by a Google
// Photos library, so Immich-compatible clients (and, best-effort, the official
// Immich mobile app) can browse, view and back up against gpix instead of a
// self-hosted Immich server.
//
// IMPORTANT: this is a first-iteration, best-effort mapping. The official Immich
// app version-checks the server and its timeline/sync API evolves quickly, so
// some flows will need tuning against a real device. Endpoints implemented here
// cover auth, server info, the timeline (buckets + bucket), asset metadata,
// thumbnails, original download, and upload (backup).
package immich

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"gpix/pkg/gpmc"
	"gpix/pkg/library"
	"gpix/pkg/mediacrypt"
)

// Config configures the Immich-compatible server.
type Config struct {
	Listen       string
	Username     string // display name
	Email        string // login email shown in clients; defaults from Username
	PasswordHash string // bcrypt
	SignKey      []byte // for bearer tokens (>=32 bytes)
	ServerURL    string
	TempDir      string

	// Advertised version. The mobile app compares this against its own; if the
	// app refuses to connect, bump this to match the app's expected server.
	VersionMajor int
	VersionMinor int
	VersionPatch int
}

// Server is the Immich-compatible HTTP server.
type Server struct {
	cfg     Config
	gp      *gpmc.Client
	lib     *library.Cache
	crypt   *mediacrypt.Manager
	log     *slog.Logger
	httpSrv *http.Server
	mux     *http.ServeMux
}

// New constructs the server.
func New(cfg Config, gp *gpmc.Client, lib *library.Cache, crypt *mediacrypt.Manager, log *slog.Logger) (*Server, error) {
	if log == nil {
		log = slog.Default()
	}
	if cfg.VersionMajor == 0 && cfg.VersionMinor == 0 && cfg.VersionPatch == 0 {
		cfg.VersionMajor, cfg.VersionMinor, cfg.VersionPatch = 1, 119, 0
	}
	if cfg.Email == "" {
		if strings.Contains(cfg.Username, "@") {
			cfg.Email = cfg.Username
		} else {
			cfg.Email = cfg.Username + "@gpix.local"
		}
	}
	s := &Server{cfg: cfg, gp: gp, lib: lib, crypt: crypt, log: log}
	s.routes()
	s.httpSrv = &http.Server{
		Addr:              cfg.Listen,
		Handler:           s.withCommon(s.mux),
		ReadHeaderTimeout: 15 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
	return s, nil
}

// Handler exposes the raw handler (for tests / mounting).
func (s *Server) Handler() http.Handler { return s.withCommon(s.mux) }

func (s *Server) Run(ctx context.Context) error {
	errCh := make(chan error, 1)
	go func() {
		s.log.Info("immich api listening", "addr", s.cfg.Listen)
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

func (s *Server) routes() {
	m := http.NewServeMux()

	// Public (no auth).
	m.HandleFunc("GET /api/server/ping", s.handlePing)
	m.HandleFunc("GET /api/server-info/ping", s.handlePing)
	m.HandleFunc("GET /api/server/version", s.handleVersion)
	m.HandleFunc("GET /api/server-info/version", s.handleVersion)
	m.HandleFunc("GET /api/server/config", s.handleServerConfig)
	m.HandleFunc("GET /api/server/features", s.handleServerFeatures)
	m.HandleFunc("GET /api/server/about", s.handleServerAbout)
	m.HandleFunc("GET /api/server/storage", s.handleServerStorage)
	m.HandleFunc("GET /api/server-info/storage", s.handleServerStorage)
	m.HandleFunc("POST /api/auth/login", s.handleLogin)
	m.HandleFunc("POST /api/auth/logout", s.handleLogout)

	// Authenticated.
	m.HandleFunc("POST /api/auth/validateToken", s.requireAuth(s.handleValidateToken))
	m.HandleFunc("GET /api/users/me", s.requireAuth(s.handleUserMe))
	m.HandleFunc("GET /api/users/me/preferences", s.requireAuth(s.handleUserPreferences))
	m.HandleFunc("GET /api/timeline/buckets", s.requireAuth(s.handleTimelineBuckets))
	m.HandleFunc("GET /api/timeline/bucket", s.requireAuth(s.handleTimelineBucket))
	m.HandleFunc("GET /api/assets/{id}", s.requireAuth(s.handleAsset))
	m.HandleFunc("GET /api/assets/{id}/thumbnail", s.requireAuth(s.handleAssetThumbnail))
	m.HandleFunc("GET /api/assets/{id}/original", s.requireAuth(s.handleAssetOriginal))
	m.HandleFunc("GET /api/assets/{id}/video/playback", s.requireAuth(s.handleAssetOriginal))
	m.HandleFunc("POST /api/assets", s.requireAuth(s.handleUpload))
	m.HandleFunc("PUT /api/assets", s.requireAuth(s.handleUpload))
	m.HandleFunc("POST /api/assets/bulk-upload-check", s.requireAuth(s.handleBulkUploadCheck))
	m.HandleFunc("POST /api/search/metadata", s.requireAuth(s.handleSearchMetadata))

	s.mux = m
}

// withCommon adds permissive CORS (the app/web client sets custom headers) and
// a fallback 404 JSON for unknown /api routes.
func (s *Server) withCommon(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET,POST,PUT,DELETE,PATCH,OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization,Content-Type,x-api-key,x-immich-checksum,x-immich-user-token")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		defer func() {
			if rec := recover(); rec != nil {
				s.log.Error("immich panic", "recover", rec, "path", r.URL.Path)
				writeJSON(w, http.StatusInternalServerError, errorResponse{Message: "internal error", StatusCode: 500, Error: "InternalError"})
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// --- auth ---

func (s *Server) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tok := bearerToken(r)
		if tok == "" || !s.verifyToken(tok) {
			writeJSON(w, http.StatusUnauthorized, errorResponse{Message: "Authentication required", StatusCode: 401, Error: "Unauthorized"})
			return
		}
		next(w, r)
	}
}

func bearerToken(r *http.Request) string {
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		return strings.TrimSpace(h[len("Bearer "):])
	}
	if k := r.Header.Get("x-api-key"); k != "" {
		return k
	}
	if c, err := r.Cookie("immich_access_token"); err == nil {
		return c.Value
	}
	return ""
}

func (s *Server) signToken(ttl time.Duration) string {
	exp := time.Now().Add(ttl).Unix()
	msg := s.userID() + "|" + strconv.FormatInt(exp, 10)
	mac := hmac.New(sha256.New, s.cfg.SignKey)
	mac.Write([]byte(msg))
	return base64.RawURLEncoding.EncodeToString([]byte(msg)) + "." +
		base64.RawURLEncoding.EncodeToString(mac.Sum(nil)[:16])
}

func (s *Server) verifyToken(tok string) bool {
	parts := strings.SplitN(tok, ".", 2)
	if len(parts) != 2 {
		return false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return false
	}
	gotMac, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, s.cfg.SignKey)
	mac.Write(payload)
	if !hmac.Equal(mac.Sum(nil)[:16], gotMac) {
		return false
	}
	fields := strings.Split(string(payload), "|")
	if len(fields) != 2 {
		return false
	}
	exp, err := strconv.ParseInt(fields[1], 10, 64)
	return err == nil && time.Now().Unix() <= exp
}

// userID returns a stable UUID-shaped identifier for the single gpix user.
func (s *Server) userID() string {
	sum := sha256.Sum256([]byte("gpix-user|" + s.cfg.Username))
	return uuidFromBytes(sum[:])
}

func uuidFromBytes(b []byte) string {
	h := hex.EncodeToString(b)
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

// assetID/decodeAssetID keep media keys URL-safe in asset paths.
func assetID(mediaKey string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(mediaKey))
}

func decodeAssetID(id string) (string, bool) {
	b, err := base64.RawURLEncoding.DecodeString(id)
	if err != nil {
		return "", false
	}
	return string(b), true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
