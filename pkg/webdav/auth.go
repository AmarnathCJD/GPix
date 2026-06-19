package webdav

import (
	"crypto/subtle"
	"net/http"
	"time"

	"golang.org/x/crypto/bcrypt"
)

func (s *Server) basicAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok {
			s.challenge(w)
			return
		}

		// Damp timing on both username and password mismatch so a wrong
		// username is indistinguishable from a wrong password.
		userOK := subtle.ConstantTimeCompare([]byte(user), []byte(s.cfg.Username)) == 1
		passOK := bcrypt.CompareHashAndPassword([]byte(s.cfg.PasswordHash), []byte(pass)) == nil
		if !userOK || !passOK {
			time.Sleep(300 * time.Millisecond)
			s.challenge(w)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) challenge(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", `Basic realm="gpix"`)
	http.Error(w, "unauthorized", http.StatusUnauthorized)
}
