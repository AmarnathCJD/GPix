package webdav

import (
	"errors"
	"os"
	"strconv"
)

type Config struct {
	Username      string
	PasswordHash  string
	EncPassphrase string
	ListPageSize  int
}

func LoadFromEnv() (Config, error) {
	cfg := Config{
		Username:      os.Getenv("WEBDAV_USERNAME"),
		PasswordHash:  os.Getenv("WEBDAV_PASSWORD_HASH"),
		EncPassphrase: os.Getenv("WEBDAV_ENC_PASSPHRASE"),
		ListPageSize:  100,
	}
	if v := os.Getenv("WEBDAV_LIST_PAGE_SIZE"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.ListPageSize = n
		}
	}
	if cfg.Username == "" {
		return cfg, errors.New("webdav: WEBDAV_USERNAME is required")
	}
	if cfg.PasswordHash == "" {
		return cfg, errors.New("webdav: WEBDAV_PASSWORD_HASH is required")
	}
	return cfg, nil
}
