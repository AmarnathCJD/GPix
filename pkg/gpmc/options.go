package gpmc

import "net/http"

type Quality int

const (
	QualityOriginal Quality = iota
	QualitySaver
	QualityUseQuota
)

type UploadOpts struct {
	Quality            Quality
	Force              bool
	Concurrency        int
	Recursive          bool
	DeleteAfter        bool
	OverrideName       string
	EncryptPassphrase  string
}

type Option func(*Client)

func WithHTTPClient(h *http.Client) Option {
	return func(c *Client) { c.httpc = h }
}

func WithDeviceProfile(p DeviceProfile) Option {
	return func(c *Client) { c.profile = p }
}

func WithLanguage(lang string) Option {
	return func(c *Client) { c.language = lang }
}

func WithProxy(proxy string) Option {
	return func(c *Client) { c.proxy = proxy }
}
