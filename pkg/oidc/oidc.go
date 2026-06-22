// Package oidc is a small OpenID Connect (authorization-code + PKCE) client,
// used to log into gpix via Logto (or any standards-compliant OIDC provider).
// It avoids JWT verification by reading identity from the provider's userinfo
// endpoint over the back-channel after a confidential-client token exchange.
package oidc

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Config configures the OIDC client.
type Config struct {
	Issuer       string // e.g. https://xxxx.logto.app  (or full issuer URL)
	ClientID     string
	ClientSecret string
	RedirectURL  string
	Scopes       []string // defaults to openid, profile, email
}

// Client is a configured OIDC client with cached discovery.
type Client struct {
	cfg  Config
	http *http.Client

	mu        sync.Mutex
	disco     discovery
	discoTime time.Time
}

type discovery struct {
	Issuer        string `json:"issuer"`
	AuthEndpoint  string `json:"authorization_endpoint"`
	TokenEndpoint string `json:"token_endpoint"`
	UserInfoEP    string `json:"userinfo_endpoint"`
}

// Tokens is the token-endpoint response.
type Tokens struct {
	AccessToken string `json:"access_token"`
	IDToken     string `json:"id_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int    `json:"expires_in"`
}

// UserInfo is the subset of claims gpix needs.
type UserInfo struct {
	Subject       string `json:"sub"`
	Email         string `json:"email"`
	EmailVerified bool   `json:"email_verified"`
	Name          string `json:"name"`
	Username      string `json:"username"`
}

// New returns a client. It does not contact the provider until first use.
func New(cfg Config) *Client {
	if len(cfg.Scopes) == 0 {
		cfg.Scopes = []string{"openid", "profile", "email"}
	}
	cfg.Issuer = strings.TrimRight(cfg.Issuer, "/")
	return &Client{
		cfg:  cfg,
		http: &http.Client{Timeout: 20 * time.Second},
	}
}

// discover fetches and caches the provider's OIDC metadata.
func (c *Client) discover(ctx context.Context) (discovery, error) {
	c.mu.Lock()
	if c.disco.AuthEndpoint != "" && time.Since(c.discoTime) < time.Hour {
		d := c.disco
		c.mu.Unlock()
		return d, nil
	}
	c.mu.Unlock()

	// Logto serves discovery under /oidc/.well-known/...; standard providers use
	// the root. Try the standard path first, then the Logto path.
	candidates := []string{
		c.cfg.Issuer + "/.well-known/openid-configuration",
		c.cfg.Issuer + "/oidc/.well-known/openid-configuration",
	}
	var lastErr error
	for _, u := range candidates {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		resp, err := c.http.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			lastErr = fmt.Errorf("oidc discovery %s: status %d", u, resp.StatusCode)
			continue
		}
		var d discovery
		if err := json.Unmarshal(body, &d); err != nil {
			lastErr = err
			continue
		}
		if d.AuthEndpoint == "" || d.TokenEndpoint == "" {
			lastErr = fmt.Errorf("oidc discovery %s: incomplete", u)
			continue
		}
		c.mu.Lock()
		c.disco = d
		c.discoTime = time.Now()
		c.mu.Unlock()
		return d, nil
	}
	return discovery{}, fmt.Errorf("oidc discovery failed: %w", lastErr)
}

// PKCE returns a fresh code verifier and its S256 challenge.
func PKCE() (verifier, challenge string) {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	verifier = base64.RawURLEncoding.EncodeToString(b)
	sum := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(sum[:])
	return
}

// RandomState returns a URL-safe random string for state/nonce.
func RandomState() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// AuthCodeURL builds the authorization request URL.
func (c *Client) AuthCodeURL(ctx context.Context, state, nonce, challenge string) (string, error) {
	d, err := c.discover(ctx)
	if err != nil {
		return "", err
	}
	q := url.Values{}
	q.Set("response_type", "code")
	q.Set("client_id", c.cfg.ClientID)
	q.Set("redirect_uri", c.cfg.RedirectURL)
	q.Set("scope", strings.Join(c.cfg.Scopes, " "))
	q.Set("state", state)
	q.Set("nonce", nonce)
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	sep := "?"
	if strings.Contains(d.AuthEndpoint, "?") {
		sep = "&"
	}
	return d.AuthEndpoint + sep + q.Encode(), nil
}

// Exchange swaps an authorization code for tokens (confidential client +
// client_secret_basic + PKCE verifier).
func (c *Client) Exchange(ctx context.Context, code, verifier string) (Tokens, error) {
	d, err := c.discover(ctx)
	if err != nil {
		return Tokens{}, err
	}
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", c.cfg.RedirectURL)
	form.Set("code_verifier", verifier)
	form.Set("client_id", c.cfg.ClientID)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.TokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return Tokens{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth(url.QueryEscape(c.cfg.ClientID), url.QueryEscape(c.cfg.ClientSecret))
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return Tokens{}, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return Tokens{}, fmt.Errorf("oidc token exchange: status %d: %s", resp.StatusCode, string(body))
	}
	var t Tokens
	if err := json.Unmarshal(body, &t); err != nil {
		return Tokens{}, err
	}
	if t.AccessToken == "" {
		return Tokens{}, fmt.Errorf("oidc token exchange: no access token")
	}
	return t, nil
}

// UserInfo fetches identity claims using the access token.
func (c *Client) UserInfo(ctx context.Context, accessToken string) (UserInfo, error) {
	d, err := c.discover(ctx)
	if err != nil {
		return UserInfo{}, err
	}
	if d.UserInfoEP == "" {
		return UserInfo{}, fmt.Errorf("oidc: provider has no userinfo endpoint")
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, d.UserInfoEP, nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return UserInfo{}, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return UserInfo{}, fmt.Errorf("oidc userinfo: status %d", resp.StatusCode)
	}
	var ui UserInfo
	if err := json.Unmarshal(body, &ui); err != nil {
		return UserInfo{}, err
	}
	if ui.Subject == "" {
		return UserInfo{}, fmt.Errorf("oidc userinfo: no subject")
	}
	return ui, nil
}
