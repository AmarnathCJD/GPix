package web

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

func deriveKey(master []byte, purpose string) []byte {
	m := hmac.New(sha256.New, master)
	m.Write([]byte(purpose))
	return m.Sum(nil)
}

var (
	errBadToken = errors.New("bad token")
	errExpired  = errors.New("token expired")
)

func (s *Server) signMedia(mediaKey string, ttl time.Duration) string {
	exp := time.Now().Add(ttl).Unix()
	msg := mediaKey + "|" + strconv.FormatInt(exp, 10)
	m := hmac.New(sha256.New, s.mediaSignKey)
	m.Write([]byte(msg))
	mac := m.Sum(nil)[:16]
	return fmt.Sprintf("%d.%s.%s",
		exp,
		base64.RawURLEncoding.EncodeToString([]byte(mediaKey)),
		base64.RawURLEncoding.EncodeToString(mac))
}

func (s *Server) verifyMedia(tok string) (string, error) {
	parts := strings.SplitN(tok, ".", 3)
	if len(parts) != 3 {
		return "", errBadToken
	}
	exp, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return "", errBadToken
	}
	if time.Now().Unix() > exp {
		return "", errExpired
	}
	keyBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", errBadToken
	}
	gotMac, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return "", errBadToken
	}
	mediaKey := string(keyBytes)
	m := hmac.New(sha256.New, s.mediaSignKey)
	m.Write([]byte(mediaKey + "|" + parts[0]))
	if !hmac.Equal(m.Sum(nil)[:16], gotMac) {
		return "", errBadToken
	}
	return mediaKey, nil
}

// oauthState is the per-login OIDC state stored (signed) in a cookie between
// the authorize redirect and the callback.
type oauthState struct {
	State    string
	Nonce    string
	Verifier string
}

func (s *Server) signOAuthState(st oauthState, ttl time.Duration) string {
	exp := time.Now().Add(ttl).Unix()
	payload := st.State + "|" + st.Nonce + "|" + st.Verifier + "|" + strconv.FormatInt(exp, 10)
	m := hmac.New(sha256.New, s.sessionSignKey)
	m.Write([]byte("oauth|" + payload))
	mac := m.Sum(nil)[:16]
	return base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." + base64.RawURLEncoding.EncodeToString(mac)
}

func (s *Server) verifyOAuthState(val string) (oauthState, bool) {
	parts := strings.SplitN(val, ".", 2)
	if len(parts) != 2 {
		return oauthState{}, false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return oauthState{}, false
	}
	mac, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return oauthState{}, false
	}
	m := hmac.New(sha256.New, s.sessionSignKey)
	m.Write([]byte("oauth|" + string(payload)))
	if !hmac.Equal(m.Sum(nil)[:16], mac) {
		return oauthState{}, false
	}
	fields := strings.Split(string(payload), "|")
	if len(fields) != 4 {
		return oauthState{}, false
	}
	exp, err := strconv.ParseInt(fields[3], 10, 64)
	if err != nil || time.Now().Unix() > exp {
		return oauthState{}, false
	}
	return oauthState{State: fields[0], Nonce: fields[1], Verifier: fields[2]}, true
}

// signShareAccess issues a short-lived token proving the holder entered the
// correct password for a given share. It is stored in a cookie scoped to that
// share's URL so a recipient is not re-prompted for every asset.
func (s *Server) signShareAccess(token string, ttl time.Duration) string {
	exp := time.Now().Add(ttl).Unix()
	m := hmac.New(sha256.New, s.mediaSignKey)
	m.Write([]byte("share|" + token + "|" + strconv.FormatInt(exp, 10)))
	mac := m.Sum(nil)[:16]
	return fmt.Sprintf("%d.%s", exp, base64.RawURLEncoding.EncodeToString(mac))
}

func (s *Server) verifyShareAccess(token, val string) bool {
	parts := strings.SplitN(val, ".", 2)
	if len(parts) != 2 {
		return false
	}
	exp, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || time.Now().Unix() > exp {
		return false
	}
	mac, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return false
	}
	m := hmac.New(sha256.New, s.mediaSignKey)
	m.Write([]byte("share|" + token + "|" + parts[0]))
	return hmac.Equal(m.Sum(nil)[:16], mac)
}

func (s *Server) signSession(username string, ttl time.Duration) string {
	now := time.Now().Unix()
	exp := time.Now().Add(ttl).Unix()
	msg := username + "|" + strconv.FormatInt(now, 10) + "|" + strconv.FormatInt(exp, 10)
	m := hmac.New(sha256.New, s.sessionSignKey)
	m.Write([]byte(msg))
	mac := m.Sum(nil)[:16]
	return fmt.Sprintf("%s.%s",
		base64.RawURLEncoding.EncodeToString([]byte(msg)),
		base64.RawURLEncoding.EncodeToString(mac))
}

func (s *Server) verifySession(tok string) (username string, err error) {
	parts := strings.SplitN(tok, ".", 2)
	if len(parts) != 2 {
		return "", errBadToken
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return "", errBadToken
	}
	gotMac, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", errBadToken
	}
	m := hmac.New(sha256.New, s.sessionSignKey)
	m.Write(payload)
	if !hmac.Equal(m.Sum(nil)[:16], gotMac) {
		return "", errBadToken
	}
	fields := strings.Split(string(payload), "|")
	if len(fields) != 3 {
		return "", errBadToken
	}
	exp, err := strconv.ParseInt(fields[2], 10, 64)
	if err != nil {
		return "", errBadToken
	}
	if time.Now().Unix() > exp {
		return "", errExpired
	}
	return fields[0], nil
}
