package web

import (
	"io/fs"
	"net/http"
)

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /login", s.handleLoginForm)
	mux.HandleFunc("POST /login", s.handleLoginSubmit)
	mux.HandleFunc("POST /logout", s.requireSession(s.handleLogout))

	mux.HandleFunc("GET /auth/logto/login", s.handleLogtoLogin)
	mux.HandleFunc("GET /auth/logto/callback", s.handleLogtoCallback)

	mux.HandleFunc("GET /{$}", s.requireSession(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/browse", http.StatusSeeOther)
	}))
	mux.HandleFunc("GET /browse", s.requireSession(s.handleBrowse))
	mux.HandleFunc("GET /view/{key}", s.requireSession(s.handleView))
	mux.HandleFunc("GET /thumb/{key}", s.requireSession(s.handleThumb))
	mux.HandleFunc("GET /upload", s.requireSession(s.handleUploadForm))
	mux.HandleFunc("POST /upload", s.requireSession(s.handleUploadSubmit))
	mux.HandleFunc("POST /delete/{key}", s.requireSession(s.handleDelete))
	mux.HandleFunc("GET /api/upload-progress/{id}", s.requireSession(s.handleProgressSSE))

	mux.HandleFunc("GET /settings/gateways", s.requireSession(s.handleGateways))
	mux.HandleFunc("POST /settings/gateways/regenerate", s.requireSession(s.handleGatewaysRegenerate))
	mux.HandleFunc("POST /settings/gateways/clear", s.requireSession(s.handleGatewaysClear))
	mux.HandleFunc("POST /settings/gateways/encryption", s.requireSession(s.handleEncryptionToggle))
	mux.HandleFunc("GET /settings/gateways/encryption-key", s.requireSession(s.handleEncryptionKeyBackup))

	mux.HandleFunc("GET /stream/{token}", s.handleStream)
	mux.HandleFunc("GET /raw/{token}", s.handleRaw)

	// Share management (authenticated).
	mux.HandleFunc("POST /share/create", s.requireSession(s.handleShareCreate))
	mux.HandleFunc("GET /settings/shares", s.requireSession(s.handleSharesList))
	mux.HandleFunc("POST /share/revoke/{token}", s.requireSession(s.handleShareRevoke))

	// Public share links (no session).
	mux.HandleFunc("GET /s/{token}", s.handleSharePage)
	mux.HandleFunc("POST /s/{token}", s.handleSharePassword)
	mux.HandleFunc("GET /s/{token}/thumb/{idx}", s.handleShareThumb)
	mux.HandleFunc("GET /s/{token}/raw/{idx}", s.handleShareRaw)

	sub, _ := fs.Sub(staticFS, "static")
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(sub)))

	return s.recoverPanic(s.logRequest(s.securityHeaders(mux)))
}
