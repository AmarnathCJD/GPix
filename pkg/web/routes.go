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

	mux.HandleFunc("GET /{$}", s.requireSession(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/browse", http.StatusSeeOther)
	}))
	mux.HandleFunc("GET /browse", s.requireSession(s.handleBrowse))
	mux.HandleFunc("GET /view/{key}", s.requireSession(s.handleView))
	mux.HandleFunc("GET /thumb/{key}", s.requireSession(s.handleThumb))
	mux.HandleFunc("GET /upload", s.requireSession(s.handleUploadForm))
	mux.HandleFunc("POST /upload", s.requireSession(s.handleUploadSubmit))
	mux.HandleFunc("POST /delete/{key}", s.requireSession(s.handleDelete))
	mux.HandleFunc("POST /api/bulk/delete", s.requireSession(s.handleBulkDelete))
	mux.HandleFunc("GET /api/bulk/download", s.requireSession(s.handleBulkDownload))
	mux.HandleFunc("GET /api/albums", s.requireSession(s.handleAlbumsList))
	mux.HandleFunc("POST /api/albums", s.requireSession(s.handleAlbumCreate))
	mux.HandleFunc("POST /api/albums/add", s.requireSession(s.handleAlbumAdd))
	mux.HandleFunc("GET /api/upload-progress/{id}", s.requireSession(s.handleProgressSSE))

	mux.HandleFunc("GET /stream/{token}", s.handleStream)
	mux.HandleFunc("GET /raw/{token}", s.handleRaw)

	sub, _ := fs.Sub(staticFS, "static")
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(sub)))

	for prefix, h := range s.extraMounts {
		mux.Handle(prefix, h)
	}

	return s.recoverPanic(s.logRequest(s.securityHeaders(mux)))
}
