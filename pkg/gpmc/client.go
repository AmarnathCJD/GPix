package gpmc

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"gpix/pkg/gpmc/albumstore"
)

type Client struct {
	httpc    *http.Client
	authData string
	profile  DeviceProfile
	language string
	proxy    string

	tokMu   sync.Mutex
	token   string
	expires time.Time

	drive driveTokenCache

	albums *albumstore.Store

	syncMu sync.Mutex
	sync   *syncCache
}

func (c *Client) AlbumStore() *albumstore.Store { return c.albums }

func (c *Client) HTTPClient() *http.Client     { return c.httpc }
func (c *Client) DeviceProfile() DeviceProfile { return c.profile }
func (c *Client) AuthData() string             { return c.authData }
func (c *Client) Language() string             { return c.language }

func (c *Client) BearerToken(ctx context.Context) (string, error) { return c.bearer(ctx) }

func New(authData string, opts ...Option) (*Client, error) {
	authData = strings.TrimSpace(authData)
	if authData == "" {
		return nil, errors.New("gpmc: empty auth_data")
	}
	c := &Client{
		authData: authData,
		profile:  DefaultPixelXL(),
		language: "en_US",
	}
	for _, opt := range opts {
		opt(c)
	}
	if c.httpc == nil {
		tr := &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			TLSHandshakeTimeout:   30 * time.Second,
			ResponseHeaderTimeout: 60 * time.Second,
			MaxIdleConns:          16,
			IdleConnTimeout:       90 * time.Second,
		}
		if c.proxy != "" {
			if u, err := url.Parse(c.proxy); err == nil {
				tr.Proxy = http.ProxyURL(u)
			}
		}
		c.httpc = &http.Client{Transport: tr}
	}
	return c, nil
}
