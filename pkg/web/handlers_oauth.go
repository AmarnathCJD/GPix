package web

import (
	"net/http"
	"time"

	"gpix/pkg/oidc"
	"gpix/pkg/users"
)

const oauthCookieName = "gpix_oauth"

func (s *Server) loginError(w http.ResponseWriter, msg string) {
	s.render(w, "login", pageData{Error: msg, LogtoEnabled: s.cfg.LogtoEnabled()})
}

// handleLogtoLogin starts the OIDC authorization-code + PKCE flow.
func (s *Server) handleLogtoLogin(w http.ResponseWriter, r *http.Request) {
	if s.oidc == nil {
		http.NotFound(w, r)
		return
	}
	verifier, challenge := oidc.PKCE()
	state := oidc.RandomState()
	nonce := oidc.RandomState()

	ctx, cancel := withTimeout(r.Context(), 15*time.Second)
	defer cancel()
	authURL, err := s.oidc.AuthCodeURL(ctx, state, nonce, challenge)
	if err != nil {
		s.log.Error("logto auth url", "err", err)
		s.loginError(w, "Could not reach the login provider.")
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     oauthCookieName,
		Value:    s.signOAuthState(oauthState{State: state, Nonce: nonce, Verifier: verifier}, 10*time.Minute),
		Path:     "/auth/logto",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   600,
	})
	http.Redirect(w, r, authURL, http.StatusSeeOther)
}

// handleLogtoCallback completes the OIDC flow: validates state, exchanges the
// code, fetches identity, applies the allowlist + max-users policy, and starts
// a session.
func (s *Server) handleLogtoCallback(w http.ResponseWriter, r *http.Request) {
	if s.oidc == nil {
		http.NotFound(w, r)
		return
	}
	if e := r.URL.Query().Get("error"); e != "" {
		s.loginError(w, "Sign-in was cancelled or failed.")
		return
	}
	c, err := r.Cookie(oauthCookieName)
	if err != nil {
		s.loginError(w, "Your sign-in session expired. Please try again.")
		return
	}
	st, ok := s.verifyOAuthState(c.Value)
	if !ok || r.URL.Query().Get("state") != st.State {
		s.loginError(w, "Invalid sign-in request. Please try again.")
		return
	}
	// Clear the one-time oauth cookie.
	http.SetCookie(w, &http.Cookie{Name: oauthCookieName, Value: "", Path: "/auth/logto", MaxAge: -1})

	ctx, cancel := withTimeout(r.Context(), 20*time.Second)
	defer cancel()

	tokens, err := s.oidc.Exchange(ctx, r.URL.Query().Get("code"), st.Verifier)
	if err != nil {
		s.log.Error("logto exchange", "err", err)
		s.loginError(w, "Could not complete sign-in.")
		return
	}
	ui, err := s.oidc.UserInfo(ctx, tokens.AccessToken)
	if err != nil {
		s.log.Error("logto userinfo", "err", err)
		s.loginError(w, "Could not read your profile.")
		return
	}

	email := ui.Email
	name := firstNonEmpty(ui.Name, ui.Username, email, ui.Subject)

	// Registration policy for first-time sign-ins.
	if s.users != nil {
		if _, gerr := s.users.Get(ctx, ui.Subject); gerr != nil {
			if !users.Allowlisted(email, s.cfg.SignupAllowlist) {
				s.loginError(w, "This account is not on the allowlist.")
				return
			}
			if s.cfg.MaxUsers > 0 {
				if n, _ := s.users.Count(ctx); n >= s.cfg.MaxUsers {
					s.loginError(w, "Registration is closed (user limit reached).")
					return
				}
			}
			if cerr := s.users.Create(ctx, users.User{Subject: ui.Subject, Email: email, Name: name}); cerr != nil {
				s.log.Error("logto create user", "err", cerr)
				s.loginError(w, "Could not create your account.")
				return
			}
		}
	}

	sessUser := email
	if sessUser == "" {
		sessUser = ui.Subject
	}
	ttl := time.Duration(s.cfg.SessionDays) * 24 * time.Hour
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    s.signSession(sessUser, ttl),
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   int(ttl.Seconds()),
	})
	http.Redirect(w, r, "/browse", http.StatusSeeOther)
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}
