package bridge

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"

	"github.com/amarnathcjd/gogram/telegram"
)

type Config struct {
	BotToken      string
	APIID         int32
	APIHash       string
	OwnerID       int64
	SessionFile   string
	TempDir       string
	MaxConcurrent int
	Proxy         telegram.Proxy
	ProxyURL      string
}

func LoadConfigFromEnv() (Config, error) {
	cfg := Config{
		BotToken:    os.Getenv("TG_BOT_TOKEN"),
		APIHash:     os.Getenv("TG_API_HASH"),
		SessionFile: getenvDefault("TG_SESSION_FILE", "gpixbot.session"),
		TempDir:     getenvDefault("TG_TEMP_DIR", os.TempDir()),
	}
	if cfg.BotToken == "" {
		return cfg, errors.New("TG_BOT_TOKEN is required")
	}
	if cfg.APIHash == "" {
		return cfg, errors.New("TG_API_HASH is required")
	}
	apiID, err := strconv.ParseInt(os.Getenv("TG_API_ID"), 10, 32)
	if err != nil || apiID == 0 {
		return cfg, errors.New("TG_API_ID must be a non-zero integer")
	}
	cfg.APIID = int32(apiID)

	owner, err := strconv.ParseInt(os.Getenv("TG_OWNER_ID"), 10, 64)
	if err != nil || owner == 0 {
		return cfg, errors.New("TG_OWNER_ID must be a non-zero integer (your Telegram user id)")
	}
	cfg.OwnerID = owner

	cfg.MaxConcurrent = 2
	if v := os.Getenv("TG_MAX_CONCURRENT"); v != "" {
		n, err := strconv.Atoi(v)
		if err == nil && n > 0 {
			cfg.MaxConcurrent = n
		}
	}

	if v := os.Getenv("TG_PROXY"); v != "" {
		p, err := ParseProxyURL(v)
		if err != nil {
			return cfg, fmt.Errorf("TG_PROXY: %w", err)
		}
		cfg.Proxy = p
		cfg.ProxyURL = v
	}

	return cfg, nil
}

func ParseProxyURL(raw string) (telegram.Proxy, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("parse url: %w", err)
	}
	if u.Host == "" {
		return nil, errors.New("missing host:port")
	}
	host := u.Hostname()
	portStr := u.Port()
	if portStr == "" {
		return nil, errors.New("missing port")
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return nil, fmt.Errorf("invalid port %q: %w", portStr, err)
	}
	base := telegram.BaseProxy{Host: host, Port: port}

	username := ""
	password := ""
	if u.User != nil {
		username = u.User.Username()
		password, _ = u.User.Password()
	}

	switch u.Scheme {
	case "socks5", "socks5h":
		return &telegram.Socks5Proxy{BaseProxy: base, Username: username, Password: password}, nil
	case "socks4":
		return &telegram.Socks4Proxy{BaseProxy: base, UserID: username}, nil
	case "http", "https":
		return &telegram.HttpProxy{BaseProxy: base, Username: username, Password: password}, nil
	case "mtproxy", "mtproto":
		if username == "" {
			return nil, errors.New("mtproxy URL needs the secret in the userinfo position: mtproxy://<secret>@host:port")
		}
		return &telegram.MTProxy{BaseProxy: base, Secret: username}, nil
	default:
		return nil, fmt.Errorf("unknown proxy scheme %q (supported: socks5, socks4, http, mtproxy)", u.Scheme)
	}
}

func getenvDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
