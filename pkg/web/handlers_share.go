package web

import (
	"net/http"
	"strconv"
	"time"

	"gpix/pkg/share"
)

// baseURL returns the externally-visible base URL: the configured ServerURL if
// set, otherwise derived from the current request.
func (s *Server) baseURL(r *http.Request) string {
	if s.cfg.ServerURL != "" {
		return s.cfg.ServerURL
	}
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

func shareCookieName(token string) string { return "gpix_share_" + token }

// --- authenticated management ---

// handleShareCreate accepts one or more selected media keys (repeated `key`
// form fields) and creates a single share link covering all of them. Item
// names and types are resolved server-side from the library cache.
func (s *Server) handleShareCreate(w http.ResponseWriter, r *http.Request) {
	if s.share == nil {
		http.Error(w, "sharing not enabled", http.StatusNotFound)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	keys := r.Form["key"]
	if len(keys) == 0 {
		http.Error(w, "no items selected", http.StatusBadRequest)
		return
	}

	items := make([]share.ShareItem, 0, len(keys))
	for _, k := range keys {
		name, isVideo := k, false
		if s.lib != nil {
			if it, ok, _ := s.lib.Get(r.Context(), k); ok {
				display, class, _ := classifyItem(it.Filename, it.Kind)
				name, isVideo = display, class == classVideo
			}
		}
		items = append(items, share.ShareItem{MediaKey: k, FileName: name, IsVideo: isVideo})
	}

	var ttl time.Duration
	if h := r.FormValue("expiry_hours"); h != "" {
		if n, err := strconv.Atoi(h); err == nil && n > 0 {
			ttl = time.Duration(n) * time.Hour
		}
	}
	var maxDl int64
	if m := r.FormValue("max_downloads"); m != "" {
		if n, err := strconv.ParseInt(m, 10, 64); err == nil && n > 0 {
			maxDl = n
		}
	}
	if _, err := s.share.Create(r.Context(), share.CreateParams{
		Title:         r.FormValue("title"),
		Items:         items,
		Password:      r.FormValue("password"),
		TTL:           ttl,
		MaxDownloads:  maxDl,
		AllowOriginal: r.FormValue("allow_original") != "0",
	}); err != nil {
		s.log.Error("share create", "err", err)
		http.Error(w, "could not create share: "+err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/settings/shares", http.StatusSeeOther)
}

func (s *Server) handleSharesList(w http.ResponseWriter, r *http.Request) {
	if s.share == nil {
		http.Error(w, "sharing not enabled", http.StatusNotFound)
		return
	}
	shares, err := s.share.List(r.Context())
	if err != nil {
		s.render(w, "error", pageData{User: userFromCtx(r.Context()), Title: "shares", Message: err.Error()})
		return
	}
	base := s.baseURL(r)
	items := make([]shareItem, 0, len(shares))
	for _, sh := range shares {
		title := sh.Title
		if title == "" {
			if len(sh.Items) == 1 {
				title = sh.Items[0].FileName
			} else if len(sh.Items) > 1 {
				title = sh.Items[0].FileName + " +" + strconv.Itoa(len(sh.Items)-1) + " more"
			}
		}
		items = append(items, shareItem{
			Token:        sh.Token,
			URL:          base + "/s/" + sh.Token,
			Title:        title,
			Count:        len(sh.Items),
			HasPassword:  sh.HasPassword,
			ExpiresLabel: expiresLabel(sh.ExpiresAt),
			Downloads:    sh.Downloads,
			MaxDownloads: sh.MaxDownloads,
			CreatedAt:    sh.CreatedAt,
		})
	}
	s.render(w, "shares", pageData{User: userFromCtx(r.Context()), Title: "Shares", Shares: items})
}

func (s *Server) handleShareRevoke(w http.ResponseWriter, r *http.Request) {
	if s.share == nil {
		http.Error(w, "sharing not enabled", http.StatusNotFound)
		return
	}
	if token := r.PathValue("token"); token != "" {
		_ = s.share.Delete(r.Context(), token)
	}
	http.Redirect(w, r, "/settings/shares", http.StatusSeeOther)
}

func expiresLabel(t time.Time) string {
	if t.IsZero() {
		return "Never"
	}
	if time.Now().After(t) {
		return "Expired"
	}
	return t.Format("Jan 2, 2006 15:04")
}

// --- public share endpoints (no session) ---

func (s *Server) shareAuthorized(r *http.Request, sh share.Share) bool {
	if !sh.HasPassword {
		return true
	}
	c, err := r.Cookie(shareCookieName(sh.Token))
	if err != nil {
		return false
	}
	return s.verifyShareAccess(sh.Token, c.Value)
}

func (s *Server) loadShare(w http.ResponseWriter, r *http.Request) (share.Share, bool) {
	token := r.PathValue("token")
	sh, err := s.share.Get(r.Context(), token)
	if err != nil {
		s.render(w, "share_public", pageData{SharePublic: &sharePublicView{Expired: true, Error: "This link does not exist."}})
		return share.Share{}, false
	}
	if !sh.Active(time.Now()) {
		s.render(w, "share_public", pageData{SharePublic: &sharePublicView{Token: sh.Token, Title: sh.Title, Expired: true}})
		return share.Share{}, false
	}
	return sh, true
}

func (s *Server) handleSharePage(w http.ResponseWriter, r *http.Request) {
	if s.share == nil {
		http.NotFound(w, r)
		return
	}
	sh, ok := s.loadShare(w, r)
	if !ok {
		return
	}
	view := &sharePublicView{Token: sh.Token, Title: sh.Title, AllowOriginal: sh.AllowOriginal}
	if !s.shareAuthorized(r, sh) {
		view.NeedsPassword = true
		s.render(w, "share_public", pageData{SharePublic: view})
		return
	}
	for i, it := range sh.Items {
		view.Items = append(view.Items, sharePublicItem{
			Index:    i,
			Name:     it.FileName,
			IsVideo:  it.IsVideo,
			ThumbURL: "/s/" + sh.Token + "/thumb/" + strconv.Itoa(i),
			RawURL:   "/s/" + sh.Token + "/raw/" + strconv.Itoa(i),
		})
	}
	s.render(w, "share_public", pageData{SharePublic: view})
}

func (s *Server) handleSharePassword(w http.ResponseWriter, r *http.Request) {
	if s.share == nil {
		http.NotFound(w, r)
		return
	}
	sh, ok := s.loadShare(w, r)
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	if !sh.VerifyPassword(r.FormValue("password")) {
		s.render(w, "share_public", pageData{SharePublic: &sharePublicView{
			Token: sh.Token, Title: sh.Title, NeedsPassword: true, Error: "Incorrect password.",
		}})
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     shareCookieName(sh.Token),
		Value:    s.signShareAccess(sh.Token, 12*time.Hour),
		Path:     "/s/" + sh.Token,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int((12 * time.Hour).Seconds()),
	})
	http.Redirect(w, r, "/s/"+sh.Token, http.StatusSeeOther)
}

// shareItemByIndex loads a share and resolves one item, enforcing
// active + authorized access.
func (s *Server) shareItemByIndex(r *http.Request) (share.Share, share.ShareItem, bool) {
	sh, err := s.share.Get(r.Context(), r.PathValue("token"))
	if err != nil || !sh.Active(time.Now()) || !s.shareAuthorized(r, sh) {
		return share.Share{}, share.ShareItem{}, false
	}
	idx, err := strconv.Atoi(r.PathValue("idx"))
	if err != nil || idx < 0 || idx >= len(sh.Items) {
		return share.Share{}, share.ShareItem{}, false
	}
	return sh, sh.Items[idx], true
}

func (s *Server) handleShareThumb(w http.ResponseWriter, r *http.Request) {
	if s.share == nil {
		http.NotFound(w, r)
		return
	}
	_, item, ok := s.shareItemByIndex(r)
	if !ok {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	s.serveThumb(w, r, item.MediaKey, thumbSize(r))
}

func (s *Server) handleShareRaw(w http.ResponseWriter, r *http.Request) {
	if s.share == nil {
		http.NotFound(w, r)
		return
	}
	sh, item, ok := s.shareItemByIndex(r)
	if !ok {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if !sh.AllowOriginal && r.URL.Query().Get("inline") != "1" {
		http.Error(w, "download disabled for this share", http.StatusForbidden)
		return
	}
	if r.URL.Query().Get("inline") != "1" {
		_ = s.share.RecordDownload(r.Context(), sh.Token)
	}
	attachment := r.URL.Query().Get("inline") != "1"
	s.proxyDownload(w, r, item.MediaKey, attachment)
}
