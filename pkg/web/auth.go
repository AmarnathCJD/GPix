package web

import (
	"context"
	"net/http"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const sessionCookieName = "gpix_session"

type ctxKey int

const ctxKeyUser ctxKey = 1

func userFromCtx(ctx context.Context) string {
	v, _ := ctx.Value(ctxKeyUser).(string)
	return v
}

func (s *Server) handleLoginForm(w http.ResponseWriter, r *http.Request) {
	if _, err := r.Cookie(sessionCookieName); err == nil {
		if c, _ := r.Cookie(sessionCookieName); c != nil {
			if _, err := s.verifySession(c.Value); err == nil {
				http.Redirect(w, r, "/browse", http.StatusSeeOther)
				return
			}
		}
	}
	s.render(w, "login", pageData{LogtoEnabled: s.cfg.LogtoEnabled()})
}

func (s *Server) handleLoginSubmit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.render(w, "login", pageData{Error: "bad form", LogtoEnabled: s.cfg.LogtoEnabled()})
		return
	}
	u := r.FormValue("u")
	p := r.FormValue("p")

	dampDelay := 300 * time.Millisecond
	if u != s.cfg.Username {
		time.Sleep(dampDelay)
		s.render(w, "login", pageData{Error: "invalid credentials", LogtoEnabled: s.cfg.LogtoEnabled()})
		return
	}
	if err := bcrypt.CompareHashAndPassword([]byte(s.cfg.PasswordHash), []byte(p)); err != nil {
		time.Sleep(dampDelay)
		s.render(w, "login", pageData{Error: "invalid credentials", LogtoEnabled: s.cfg.LogtoEnabled()})
		return
	}

	ttl := time.Duration(s.cfg.SessionDays) * 24 * time.Hour
	tok := s.signSession(u, ttl)
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    tok,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   int(ttl.Seconds()),
	})
	http.Redirect(w, r, "/browse", http.StatusSeeOther)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   -1,
	})
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}
