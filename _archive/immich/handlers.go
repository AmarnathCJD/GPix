package immich

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"golang.org/x/crypto/bcrypt"

	"gpix/pkg/disguise"
	"gpix/pkg/gpmc"
)

func (s *Server) handlePing(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, pingResponse{Res: "pong"})
}

func (s *Server) handleVersion(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, versionResponse{Major: s.cfg.VersionMajor, Minor: s.cfg.VersionMinor, Patch: s.cfg.VersionPatch})
}

func (s *Server) handleServerConfig(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, serverConfigResponse{
		TrashDays:       30,
		UserDeleteDelay: 7,
		IsInitialized:   true,
		IsOnboarded:     true,
		ExternalDomain:  s.cfg.ServerURL,
	})
}

func (s *Server) handleServerFeatures(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, serverFeaturesResponse{
		Search:        true,
		Trash:         true,
		PasswordLogin: true,
	})
}

func (s *Server) handleServerAbout(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, serverAboutResponse{
		Version:    versionString(s.cfg),
		Repository: "gpix",
		SourceURL:  "https://github.com/AmarnathCJD/gpix",
		Build:      "gpix",
	})
}

func (s *Server) handleServerStorage(w http.ResponseWriter, r *http.Request) {
	q, err := s.gp.GetStorageQuota(r.Context())
	resp := serverStorageResponse{DiskSize: "Unlimited", DiskAvailable: "Unlimited", DiskUse: "0 B"}
	if err == nil {
		resp.DiskUseRaw = q.UsageBytes
		resp.DiskSizeRaw = q.LimitBytes
		if q.LimitBytes > 0 {
			resp.DiskAvailableRaw = q.LimitBytes - q.UsageBytes
			resp.DiskUsagePercentage = float64(q.UsageBytes) / float64(q.LimitBytes) * 100
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

func versionString(c Config) string {
	return "v" + strconv.Itoa(c.VersionMajor) + "." + strconv.Itoa(c.VersionMinor) + "." + strconv.Itoa(c.VersionPatch)
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Message: "bad request", StatusCode: 400, Error: "BadRequest"})
		return
	}
	// Single user: the password is the gate. The email field is just a label, so
	// any email works (the Immich app requires an email-shaped value there).
	if bcrypt.CompareHashAndPassword([]byte(s.cfg.PasswordHash), []byte(req.Password)) != nil {
		time.Sleep(300 * time.Millisecond)
		writeJSON(w, http.StatusUnauthorized, errorResponse{Message: "Incorrect email or password", StatusCode: 401, Error: "Unauthorized"})
		return
	}
	email := req.Email
	if email == "" {
		email = s.cfg.Email
	}
	writeJSON(w, http.StatusCreated, loginResponse{
		AccessToken: s.signToken(30 * 24 * time.Hour),
		UserID:      s.userID(),
		UserEmail:   email,
		Name:        s.cfg.Username,
		IsAdmin:     true,
	})
}

func (s *Server) handleLogout(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"successful": true, "redirectUri": "/auth/login"})
}

func (s *Server) handleValidateToken(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, validateTokenResponse{AuthStatus: true})
}

func (s *Server) handleUserMe(w http.ResponseWriter, _ *http.Request) {
	now := isoTime(time.Now())
	writeJSON(w, http.StatusOK, userResponse{
		ID:              s.userID(),
		Email:           s.cfg.Email,
		Name:            s.cfg.Username,
		IsAdmin:         true,
		CreatedAt:       now,
		UpdatedAt:       now,
		Status:          "active",
		MemoriesEnabled: true,
		AvatarColor:     "primary",
	})
}

func (s *Server) handleUserPreferences(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"folders":  map[string]any{"enabled": false, "sidebarWeb": false},
		"memories": map[string]any{"enabled": true},
		"people":   map[string]any{"enabled": false, "sidebarWeb": false},
		"ratings":  map[string]any{"enabled": false},
		"tags":     map[string]any{"enabled": false, "sidebarWeb": false},
	})
}

// --- timeline ---

func bucketKey(t time.Time, size string) time.Time {
	t = t.UTC()
	if size == "DAY" {
		return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
	}
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
}

func (s *Server) handleTimelineBuckets(w http.ResponseWriter, r *http.Request) {
	size := r.URL.Query().Get("size")
	items, err := s.lib.All(r.Context())
	if err != nil {
		writeJSON(w, http.StatusBadGateway, errorResponse{Message: err.Error(), StatusCode: 502, Error: "BadGateway"})
		return
	}
	counts := map[time.Time]int{}
	for _, it := range items {
		counts[bucketKey(it.Mtime, size)]++
	}
	out := make([]timeBucketResponse, 0, len(counts))
	for k, c := range counts {
		out = append(out, timeBucketResponse{TimeBucket: isoTime(k), Count: c})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TimeBucket > out[j].TimeBucket })
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleTimelineBucket(w http.ResponseWriter, r *http.Request) {
	size := r.URL.Query().Get("size")
	bucket := r.URL.Query().Get("timeBucket")
	want, err := time.Parse("2006-01-02T15:04:05.000Z", bucket)
	if err != nil {
		want, err = time.Parse(time.RFC3339, bucket)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, errorResponse{Message: "bad timeBucket", StatusCode: 400, Error: "BadRequest"})
			return
		}
	}
	want = bucketKey(want, size)
	items, err := s.lib.All(r.Context())
	if err != nil {
		writeJSON(w, http.StatusBadGateway, errorResponse{Message: err.Error(), StatusCode: 502, Error: "BadGateway"})
		return
	}
	out := make([]assetResponse, 0, 64)
	for _, it := range items {
		if bucketKey(it.Mtime, size).Equal(want) {
			out = append(out, s.assetFromItem(it))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].FileCreatedAt > out[j].FileCreatedAt })
	writeJSON(w, http.StatusOK, out)
}

// --- assets ---

func (s *Server) resolveAsset(r *http.Request) (gpmc.MediaItem, bool) {
	id := r.PathValue("id")
	mediaKey, ok := decodeAssetID(id)
	if !ok {
		return gpmc.MediaItem{}, false
	}
	it, found, err := s.lib.Get(r.Context(), mediaKey)
	if err != nil || !found {
		return gpmc.MediaItem{}, false
	}
	return it, true
}

func (s *Server) handleAsset(w http.ResponseWriter, r *http.Request) {
	it, ok := s.resolveAsset(r)
	if !ok {
		writeJSON(w, http.StatusNotFound, errorResponse{Message: "Asset not found", StatusCode: 404, Error: "NotFound"})
		return
	}
	writeJSON(w, http.StatusOK, s.assetFromItem(it))
}

func (s *Server) handleAssetThumbnail(w http.ResponseWriter, r *http.Request) {
	it, ok := s.resolveAsset(r)
	if !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	s.proxyThumbnail(w, r, it.MediaKey, thumbnailSize(r))
}

func (s *Server) handleAssetOriginal(w http.ResponseWriter, r *http.Request) {
	it, ok := s.resolveAsset(r)
	if !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	rc, meta, err := s.fetchOriginal(r.Context(), it.MediaKey)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer rc.Close()
	if meta.ContentType != "" {
		w.Header().Set("Content-Type", meta.ContentType)
	}
	if meta.Size >= 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(meta.Size, 10))
	}
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, rc)
}

// handleBulkUploadCheck lets the mobile backup skip items already in the library,
// matching on the base64 SHA-1 checksum gpix already stores.
func (s *Server) handleBulkUploadCheck(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Assets []struct {
			ID       string `json:"id"`
			Checksum string `json:"checksum"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Message: "bad request", StatusCode: 400, Error: "BadRequest"})
		return
	}
	items, _ := s.lib.All(r.Context())
	have := make(map[string]string, len(items)) // checksum -> assetID
	for _, it := range items {
		have[base64.StdEncoding.EncodeToString(it.SHA1)] = assetID(it.MediaKey)
	}
	type result struct {
		ID      string `json:"id"`
		Action  string `json:"action"`
		Reason  string `json:"reason,omitempty"`
		AssetID string `json:"assetId,omitempty"`
	}
	results := make([]result, 0, len(req.Assets))
	for _, a := range req.Assets {
		if aid, dup := have[a.Checksum]; dup {
			results = append(results, result{ID: a.ID, Action: "reject", Reason: "duplicate", AssetID: aid})
		} else {
			results = append(results, result{ID: a.ID, Action: "accept"})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": results})
}

func (s *Server) handleSearchMetadata(w http.ResponseWriter, r *http.Request) {
	// Minimal: return an empty page. (Full search is not implemented.)
	writeJSON(w, http.StatusOK, map[string]any{
		"assets": map[string]any{"total": 0, "count": 0, "items": []any{}, "facets": []any{}, "nextPage": nil},
		"albums": map[string]any{"total": 0, "count": 0, "items": []any{}, "facets": []any{}},
	})
}

// --- upload (mobile backup) ---

func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	mr, err := r.MultipartReader()
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Message: "expected multipart", StatusCode: 400, Error: "BadRequest"})
		return
	}

	var (
		tmpPath  string
		fileName string
		fields   = map[string]string{}
	)
	defer func() {
		if tmpPath != "" {
			os.Remove(tmpPath)
		}
	}()

	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			writeJSON(w, http.StatusBadRequest, errorResponse{Message: err.Error(), StatusCode: 400, Error: "BadRequest"})
			return
		}
		switch part.FormName() {
		case "assetData":
			fileName = part.FileName()
			tf, terr := os.CreateTemp(s.cfg.TempDir, "gpix-immich-*"+filepath.Ext(fileName))
			if terr != nil {
				writeJSON(w, http.StatusInternalServerError, errorResponse{Message: terr.Error(), StatusCode: 500, Error: "InternalError"})
				return
			}
			tmpPath = tf.Name()
			if _, cerr := io.Copy(tf, part); cerr != nil {
				tf.Close()
				writeJSON(w, http.StatusInternalServerError, errorResponse{Message: cerr.Error(), StatusCode: 500, Error: "InternalError"})
				return
			}
			tf.Close()
		default:
			b, _ := io.ReadAll(io.LimitReader(part, 4096))
			fields[part.FormName()] = string(b)
		}
	}

	if tmpPath == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{Message: "no assetData", StatusCode: 400, Error: "BadRequest"})
		return
	}
	if v := fields["originalFileName"]; v != "" {
		fileName = v
	}
	if fileName == "" {
		fileName = filepath.Base(tmpPath)
	}

	uploadPath, commitName, cleanup, perr := s.prepareUpload(tmpPath, fileName)
	if cleanup != nil {
		defer cleanup()
	}
	if perr != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Message: perr.Error(), StatusCode: 500, Error: "InternalError"})
		return
	}

	res, err := s.gp.UploadFile(r.Context(), uploadPath, gpmc.UploadOpts{Quality: gpmc.QualityOriginal, OverrideName: commitName})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Message: err.Error(), StatusCode: 500, Error: "InternalError"})
		return
	}
	s.lib.Invalidate()

	status := "created"
	if res.Skipped {
		status = "duplicate"
	}
	writeJSON(w, http.StatusCreated, uploadResponse{ID: assetID(res.MediaKey), Status: status})
}

// prepareUpload applies the same encrypt-then-disguise pipeline as the other
// upload surfaces and returns the path/name to commit to Google Photos.
func (s *Server) prepareUpload(srcPath, name string) (uploadPath, commitName string, cleanup func(), err error) {
	uploadPath, commitName = srcPath, name
	var temps []string
	cleanup = func() {
		for _, t := range temps {
			os.Remove(t)
		}
	}

	if s.crypt != nil && s.crypt.Enabled() {
		st, serr := os.Stat(srcPath)
		if serr != nil {
			return "", "", cleanup, serr
		}
		encPath, eerr := encryptToTemp(s.cfg.TempDir, srcPath, name, st.Size(), s.crypt)
		if eerr != nil {
			return "", "", cleanup, eerr
		}
		temps = append(temps, encPath)
		wrapped, werr := wrapToTemp(s.cfg.TempDir, encPath, name)
		if werr != nil {
			return "", "", cleanup, werr
		}
		temps = append(temps, wrapped)
		return wrapped, name + ".mp4", cleanup, nil
	}

	if head, herr := readHead(srcPath, 512); herr == nil && disguise.ShouldWrap("", name, head) {
		wrapped, werr := wrapToTemp(s.cfg.TempDir, srcPath, name)
		if werr != nil {
			return "", "", cleanup, werr
		}
		temps = append(temps, wrapped)
		return wrapped, name + ".mp4", cleanup, nil
	}
	return uploadPath, commitName, cleanup, nil
}
