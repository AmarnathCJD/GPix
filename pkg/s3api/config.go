package s3api

import (
	"errors"
	"os"
	"strings"
)

type Config struct {
	Bucket        string
	AccessKey     string
	SecretKey     string
	Region        string
	EncPassphrase string
	TempDir       string
}

func LoadFromEnv() (Config, error) {
	cfg := Config{
		Bucket:        envOr("S3_BUCKET", "gpix"),
		AccessKey:     os.Getenv("S3_ACCESS_KEY"),
		SecretKey:     os.Getenv("S3_SECRET_KEY"),
		Region:        envOr("S3_REGION", "us-east-1"),
		EncPassphrase: os.Getenv("S3_ENC_PASSPHRASE"),
		TempDir:       os.Getenv("S3_TEMP_DIR"),
	}
	if err := cfg.normalize(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c *Config) normalize() error {
	c.Bucket = strings.TrimSpace(c.Bucket)
	c.AccessKey = strings.TrimSpace(c.AccessKey)
	c.SecretKey = strings.TrimSpace(c.SecretKey)
	c.Region = strings.TrimSpace(c.Region)
	if c.Bucket == "" {
		c.Bucket = "gpix"
	}
	if c.Region == "" {
		c.Region = "us-east-1"
	}
	if c.AccessKey == "" || c.SecretKey == "" {
		return errors.New("s3api: S3_ACCESS_KEY and S3_SECRET_KEY are required")
	}
	if c.TempDir == "" {
		c.TempDir = os.TempDir()
	}
	return nil
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
