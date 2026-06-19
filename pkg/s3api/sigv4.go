package s3api

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"sort"
	"strings"
)

const (
	sigV4Algorithm = "AWS4-HMAC-SHA256"
	emptyBodySHA   = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	unsignedHash   = "UNSIGNED-PAYLOAD"
)

type credScope struct {
	accessKey string
	date      string
	region    string
	service   string
}

type authParts struct {
	scope         credScope
	signedHeaders []string
	signature     string
}

// verify validates the Authorization header against the configured credentials
// using AWS SigV4. The body payload is taken from x-amz-content-sha256 rather
// than recomputed: S3 clients send UNSIGNED-PAYLOAD or a streaming hash for
// large bodies, so we trust the header value the same way the canonical request
// did when it was signed.
func (s *Server) verifySigV4(r *http.Request) apiError {
	parsed, err := parseAuthHeader(r.Header.Get("Authorization"))
	if err != (apiError{}) {
		return err
	}
	if subtle.ConstantTimeCompare([]byte(parsed.scope.accessKey), []byte(s.cfg.AccessKey)) != 1 {
		return errAccessDenied
	}

	payloadHash := r.Header.Get("X-Amz-Content-Sha256")
	if payloadHash == "" {
		payloadHash = unsignedHash
	}

	canonReq := canonicalRequest(r, parsed.signedHeaders, payloadHash)
	amzDate := r.Header.Get("X-Amz-Date")
	scopeStr := strings.Join([]string{parsed.scope.date, parsed.scope.region, parsed.scope.service, "aws4_request"}, "/")
	stringToSign := strings.Join([]string{
		sigV4Algorithm,
		amzDate,
		scopeStr,
		hexSHA256([]byte(canonReq)),
	}, "\n")

	signingKey := deriveSigningKey(s.cfg.SecretKey, parsed.scope.date, parsed.scope.region, parsed.scope.service)
	want := hex.EncodeToString(hmacSHA256(signingKey, []byte(stringToSign)))

	if subtle.ConstantTimeCompare([]byte(want), []byte(parsed.signature)) != 1 {
		return errSignatureMismatch
	}
	return apiError{}
}

func parseAuthHeader(h string) (authParts, apiError) {
	if !strings.HasPrefix(h, sigV4Algorithm+" ") {
		return authParts{}, errAccessDenied
	}
	var out authParts
	fields := strings.SplitSeq(strings.TrimPrefix(h, sigV4Algorithm+" "), ",")
	for f := range fields {
		f = strings.TrimSpace(f)
		k, v, ok := strings.Cut(f, "=")
		if !ok {
			continue
		}
		switch k {
		case "Credential":
			segs := strings.Split(v, "/")
			if len(segs) != 5 || segs[4] != "aws4_request" {
				return authParts{}, errAccessDenied
			}
			out.scope = credScope{accessKey: segs[0], date: segs[1], region: segs[2], service: segs[3]}
		case "SignedHeaders":
			out.signedHeaders = strings.Split(v, ";")
		case "Signature":
			out.signature = v
		}
	}
	if out.scope.accessKey == "" || len(out.signedHeaders) == 0 || out.signature == "" {
		return authParts{}, errSignatureMismatch
	}
	return out, apiError{}
}

func canonicalRequest(r *http.Request, signedHeaders []string, payloadHash string) string {
	var b strings.Builder
	b.WriteString(r.Method)
	b.WriteByte('\n')
	b.WriteString(canonicalURI(r))
	b.WriteByte('\n')
	b.WriteString(canonicalQuery(r))
	b.WriteByte('\n')
	b.WriteString(canonicalHeaders(r, signedHeaders))
	b.WriteByte('\n')
	b.WriteString(strings.Join(signedHeaders, ";"))
	b.WriteByte('\n')
	b.WriteString(payloadHash)
	return b.String()
}

// canonicalURI re-encodes the path exactly as SigV4 requires: each segment is
// URI-encoded, but the encoded path uses r.URL.EscapedPath which preserves the
// client's original encoding of the request target.
func canonicalURI(r *http.Request) string {
	p := r.URL.EscapedPath()
	if p == "" {
		return "/"
	}
	return p
}

func canonicalQuery(r *http.Request) string {
	q := r.URL.Query()
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		vals := q[k]
		sort.Strings(vals)
		for _, v := range vals {
			parts = append(parts, uriEncode(k, true)+"="+uriEncode(v, true))
		}
	}
	return strings.Join(parts, "&")
}

func canonicalHeaders(r *http.Request, signedHeaders []string) string {
	var b strings.Builder
	for _, h := range signedHeaders {
		b.WriteString(h)
		b.WriteByte(':')
		b.WriteString(canonicalHeaderValue(r, h))
		b.WriteByte('\n')
	}
	return b.String()
}

func canonicalHeaderValue(r *http.Request, name string) string {
	if name == "host" {
		return trimAll(r.Host)
	}
	vals := r.Header.Values(http.CanonicalHeaderKey(name))
	for i := range vals {
		vals[i] = trimAll(vals[i])
	}
	return strings.Join(vals, ",")
}

func trimAll(s string) string {
	s = strings.TrimSpace(s)
	for strings.Contains(s, "  ") {
		s = strings.ReplaceAll(s, "  ", " ")
	}
	return s
}

func deriveSigningKey(secret, date, region, service string) []byte {
	kDate := hmacSHA256([]byte("AWS4"+secret), []byte(date))
	kRegion := hmacSHA256(kDate, []byte(region))
	kService := hmacSHA256(kRegion, []byte(service))
	return hmacSHA256(kService, []byte("aws4_request"))
}

func hmacSHA256(key, data []byte) []byte {
	m := hmac.New(sha256.New, key)
	m.Write(data)
	return m.Sum(nil)
}

func hexSHA256(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// uriEncode mirrors the AWS reference implementation. Query keys/values encode
// everything except the unreserved set; the slash is encoded in queries but not
// in the path (callers handle the path separately via EscapedPath).
func uriEncode(s string, encodeSlash bool) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') ||
			c == '-' || c == '_' || c == '.' || c == '~':
			b.WriteByte(c)
		case c == '/' && !encodeSlash:
			b.WriteByte(c)
		default:
			b.WriteByte('%')
			b.WriteByte(hexUpper[c>>4])
			b.WriteByte(hexUpper[c&0xf])
		}
	}
	return b.String()
}

const hexUpper = "0123456789ABCDEF"
