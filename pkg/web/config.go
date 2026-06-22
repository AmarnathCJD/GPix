package web

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
)

type Config struct {
	Listen               string
	Username             string
	PasswordHash         string
	DeviceProfile        string
	TempDir              string
	MaxConcurrentUploads int
	SessionDays          int
	StreamTokenTTLMin    int
	SecretKey            []byte

	// DataDir is where runtime state lives (secret.key, gateways.json,
	// encryption.key/json, shares.db). Empty means "next to the config file"
	// (which is the current working directory by default). Set via GPIX_DATA_DIR.
	DataDir string

	// S3-compatible gateway (optional). Enabled when S3Listen is non-empty.
	S3Listen    string
	S3AccessKey string
	S3SecretKey string
	S3Bucket    string
	S3Region    string

	// WebDAV gateway (optional). Enabled when WebDAVListen is non-empty.
	// Authenticates against Username/PasswordHash above.
	WebDAVListen   string
	WebDAVBasePath string

	// EncryptUploads seeds the "encrypt new uploads" toggle on first run (before
	// the user flips it in the web UI). Once set via the UI, that choice wins.
	EncryptUploads bool

	// ServerURL is the externally-reachable base URL of this gpix instance
	// (e.g. https://photos.example.com), used to build absolute share links and
	// redirects. Falls back to the request's own host when empty.
	ServerURL string

	// Logto / OIDC login (optional). Enabled when LogtoEndpoint, LogtoClientID
	// and LogtoClientSecret are all set. SignupAllowlist (emails or @domains)
	// limits who may register; MaxUsers caps OIDC registrations (0 = unlimited).
	LogtoEndpoint     string
	LogtoClientID     string
	LogtoClientSecret string
	LogtoRedirect     string // defaults to ServerURL + /auth/logto/callback
	SignupAllowlist   []string
	MaxUsers          int
}

// LogtoEnabled reports whether OIDC login is configured.
func (c Config) LogtoEnabled() bool {
	return c.LogtoEndpoint != "" && c.LogtoClientID != "" && c.LogtoClientSecret != ""
}

// LoadConfig builds the configuration. Every value can come from environment
// variables (typically via .env); the gpix-web.conf file at path is optional
// and, when present, provides defaults that environment variables override.
func LoadConfig(path string) (Config, error) {
	cfg := Config{
		Listen:               "0.0.0.0:8080",
		DeviceProfile:        "pixel-xl",
		MaxConcurrentUploads: 2,
		SessionDays:          30,
		StreamTokenTTLMin:    60,
		S3Bucket:             "gpix",
		S3Region:             "us-east-1",
		WebDAVBasePath:       "/",
	}

	// Optional config file (kept for backward compatibility).
	if data, err := os.ReadFile(path); err == nil {
		if perr := parseConfFile(string(data), &cfg); perr != nil {
			return cfg, perr
		}
	} else if !os.IsNotExist(err) {
		return cfg, fmt.Errorf("read config: %w", err)
	}

	applyEnv(&cfg)

	if cfg.Username == "" {
		return cfg, errors.New("config: username is required (set GPIX_USERNAME or username in gpix-web.conf)")
	}
	if cfg.PasswordHash == "" {
		return cfg, errors.New("config: password hash is required (set GPIX_PASSWORD_HASH or password_hash; generate with `gpix -hashpw`)")
	}
	if cfg.TempDir == "" {
		cfg.TempDir = os.TempDir()
	}
	return cfg, nil
}

func parseConfFile(data string, cfg *Config) error {
	for ln, line := range strings.Split(data, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		eq := strings.IndexByte(line, '=')
		if eq <= 0 {
			return fmt.Errorf("config line %d: missing =", ln+1)
		}
		k := strings.TrimSpace(line[:eq])
		v := strings.Trim(strings.TrimSpace(line[eq+1:]), `"'`)
		applyKey(cfg, k, v)
	}
	return nil
}

// applyKey sets a single config key from its (already unquoted) string value.
func applyKey(cfg *Config, k, v string) {
	switch k {
	case "listen":
		cfg.Listen = v
	case "username":
		cfg.Username = v
	case "password_hash":
		cfg.PasswordHash = v
	case "device_profile":
		cfg.DeviceProfile = v
	case "temp_dir":
		cfg.TempDir = v
	case "data_dir":
		cfg.DataDir = v
	case "max_concurrent_uploads":
		if n, _ := strconv.Atoi(v); n > 0 {
			cfg.MaxConcurrentUploads = n
		}
	case "session_days":
		if n, _ := strconv.Atoi(v); n > 0 {
			cfg.SessionDays = n
		}
	case "stream_token_ttl_minutes":
		if n, _ := strconv.Atoi(v); n > 0 {
			cfg.StreamTokenTTLMin = n
		}
	case "s3_listen":
		cfg.S3Listen = v
	case "s3_access_key":
		cfg.S3AccessKey = v
	case "s3_secret_key":
		cfg.S3SecretKey = v
	case "s3_bucket":
		if v != "" {
			cfg.S3Bucket = v
		}
	case "s3_region":
		if v != "" {
			cfg.S3Region = v
		}
	case "webdav_listen":
		cfg.WebDAVListen = v
	case "webdav_base_path":
		if v != "" {
			cfg.WebDAVBasePath = v
		}
	case "encrypt_uploads":
		cfg.EncryptUploads = truthy(v)
	case "server_url":
		cfg.ServerURL = strings.TrimRight(v, "/")
	case "logto_endpoint":
		cfg.LogtoEndpoint = strings.TrimRight(v, "/")
	case "logto_client_id":
		cfg.LogtoClientID = v
	case "logto_client_secret":
		cfg.LogtoClientSecret = v
	case "logto_redirect":
		cfg.LogtoRedirect = v
	case "signup_allowlist":
		cfg.SignupAllowlist = splitList(v)
	case "max_users":
		if n, _ := strconv.Atoi(v); n >= 0 {
			cfg.MaxUsers = n
		}
	}
}

// splitList parses a comma- or space-separated list into trimmed entries.
func splitList(v string) []string {
	var out []string
	for _, p := range strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == ' ' }) {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// applyEnv overlays GPIX_* environment variables (plus SERVER_URL) onto cfg.
// Environment values take precedence over the config file.
func applyEnv(cfg *Config) {
	env := map[string]string{
		"listen":                   "GPIX_LISTEN",
		"username":                 "GPIX_USERNAME",
		"password_hash":            "GPIX_PASSWORD_HASH",
		"device_profile":           "GPIX_DEVICE_PROFILE",
		"temp_dir":                 "GPIX_TEMP_DIR",
		"data_dir":                 "GPIX_DATA_DIR",
		"max_concurrent_uploads":   "GPIX_MAX_CONCURRENT_UPLOADS",
		"session_days":             "GPIX_SESSION_DAYS",
		"stream_token_ttl_minutes": "GPIX_STREAM_TOKEN_TTL_MIN",
		"s3_listen":                "GPIX_S3_LISTEN",
		"s3_access_key":            "GPIX_S3_ACCESS_KEY",
		"s3_secret_key":            "GPIX_S3_SECRET_KEY",
		"s3_bucket":                "GPIX_S3_BUCKET",
		"s3_region":                "GPIX_S3_REGION",
		"webdav_listen":            "GPIX_WEBDAV_LISTEN",
		"webdav_base_path":         "GPIX_WEBDAV_BASE_PATH",
		"encrypt_uploads":          "GPIX_ENCRYPT_UPLOADS",
		"server_url":               "GPIX_SERVER_URL",
		"logto_endpoint":           "GPIX_LOGTO_ENDPOINT",
		"logto_client_id":          "GPIX_LOGTO_CLIENT_ID",
		"logto_client_secret":      "GPIX_LOGTO_CLIENT_SECRET",
		"logto_redirect":           "GPIX_LOGTO_REDIRECT",
		"signup_allowlist":         "GPIX_SIGNUP_ALLOWLIST",
		"max_users":                "GPIX_MAX_USERS",
	}
	for key, envName := range env {
		if v, ok := os.LookupEnv(envName); ok {
			applyKey(cfg, key, v)
		}
	}
	// SERVER_URL (without the GPIX_ prefix) is the documented name and wins last.
	if v, ok := os.LookupEnv("SERVER_URL"); ok {
		cfg.ServerURL = strings.TrimRight(v, "/")
	}
}

func truthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "true", "1", "yes", "on":
		return true
	}
	return false
}
