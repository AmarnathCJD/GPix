package web

import (
	"bytes"
	"fmt"
	"html/template"
	"net/http"
	"time"
)

func (s *Server) loadTemplates() error {
	funcMap := template.FuncMap{
		"fmtBytes": fmtBytes,
		"fmtDate":  fmtDate,
		"kbps":     func(b int) int { return b / 1000 },
		"fileExt": func(name string) string {
			i := len(name) - 1
			for ; i >= 0; i-- {
				if name[i] == '.' {
					break
				}
			}
			if i < 0 || i == len(name)-1 {
				return "FILE"
			}
			ext := name[i+1:]
			if len(ext) > 5 {
				return "FILE"
			}
			out := make([]byte, len(ext))
			for j := 0; j < len(ext); j++ {
				c := ext[j]
				if c >= 'a' && c <= 'z' {
					c -= 32
				}
				out[j] = c
			}
			return string(out)
		},
	}
	pages := []string{"login", "browse", "view", "upload", "error", "gateways", "shares", "share_public"}
	s.pageTmpls = make(map[string]*template.Template, len(pages))
	layout, err := tmplFS.ReadFile("templates/layout.html")
	if err != nil {
		return err
	}
	for _, name := range pages {
		body, err := tmplFS.ReadFile("templates/" + name + ".html")
		if err != nil {
			return err
		}
		t := template.New(name).Funcs(funcMap)
		if _, err := t.Parse(string(layout)); err != nil {
			return err
		}
		if _, err := t.Parse(string(body)); err != nil {
			return err
		}
		s.pageTmpls[name] = t
	}
	return nil
}

type pageData struct {
	User         string
	CSRF         string
	Items        []listingItem
	NextCursor   string
	Title        string
	Message      string
	Error        string
	Filename     string
	MediaKey     string
	IsVideo      bool
	IsDisguised  bool
	MediaClass   string // "photo" | "video" | "file"
	Encrypted    bool
	OriginalName string
	DisplayKind  string
	Mtime        time.Time
	SizeBytes    int64
	StreamURL    string
	RawToken     string
	AbsStreamURL string
	Qualities    []qualityChoice
	HasQualities bool

	Gateways *gatewaysView

	Shares      []shareItem
	SharePublic *sharePublicView
}

type shareItem struct {
	Token        string
	URL          string
	FileName     string
	IsVideo      bool
	HasPassword  bool
	ExpiresLabel string
	Downloads    int64
	MaxDownloads int64
	CreatedAt    time.Time
}

type sharePublicView struct {
	Token         string
	FileName      string
	IsVideo       bool
	NeedsPassword bool
	Expired       bool
	AllowOriginal bool
	Error         string
}

// gatewaysView models the Connections settings page.
type gatewaysView struct {
	S3Enabled   bool
	S3Endpoint  string
	S3Region    string
	S3Bucket    string
	S3AccessKey string
	S3SecretKey string
	HasS3Keys   bool

	WebDAVEnabled  bool
	WebDAVEndpoint string
	WebDAVUsername string
	WebDAVPassword string
	HasWebDAVPass  bool

	EncryptionAvailable   bool
	EncryptionEnabled     bool
	EncryptionFingerprint string

	// JustGenerated is "s3" or "webdav" right after a regenerate, so the page
	// can reveal the new secret with a "save this now" prompt. Empty otherwise.
	JustGenerated string
	Notice        string
}

type qualityChoice struct {
	Index        int
	Label        string
	Width        int
	Height       int
	Bandwidth    int
	StreamURL    string
	AbsStreamURL string
}

func (p pageData) LevelURLsJSON() template.JS {
	if len(p.Qualities) == 0 {
		return template.JS("{}")
	}
	var b []byte
	b = append(b, '{')
	for i, q := range p.Qualities {
		if i > 0 {
			b = append(b, ',')
		}
		b = append(b, '"')
		b = append(b, []byte(fmt.Sprintf("%d", q.Index))...)
		b = append(b, '"', ':', '"')
		for _, c := range []byte(q.StreamURL) {
			switch c {
			case '"', '\\', '<', '>', '&':
				b = append(b, '\\', 'u', '0', '0')
				const hex = "0123456789abcdef"
				b = append(b, hex[c>>4], hex[c&0xf])
			default:
				b = append(b, c)
			}
		}
		b = append(b, '"')
	}
	b = append(b, '}')
	return template.JS(b)
}

type listingItem struct {
	MediaKey    string
	Filename    string
	DisplayName string
	Kind        int
	Class       string // "photo" | "video" | "file"
	IsDisguised bool
	DisplayKind string
	SizeBytes   int64
	Mtime       time.Time
}

func (s *Server) render(w http.ResponseWriter, name string, data pageData) {
	t, ok := s.pageTmpls[name]
	if !ok {
		http.Error(w, "no template "+name, 500)
		return
	}
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, name, data); err != nil {
		s.log.Error("render", "template", name, "err", err)
		http.Error(w, "template error: "+err.Error(), 500)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = buf.WriteTo(w)
}

func fmtBytes(n int64) string {
	const (
		KB = 1 << 10
		MB = 1 << 20
		GB = 1 << 30
	)
	switch {
	case n >= GB:
		return fmt.Sprintf("%.2f GB", float64(n)/float64(GB))
	case n >= MB:
		return fmt.Sprintf("%.1f MB", float64(n)/float64(MB))
	case n >= KB:
		return fmt.Sprintf("%.0f KB", float64(n)/float64(KB))
	default:
		return fmt.Sprintf("%d B", n)
	}
}

func fmtDate(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	return t.Format("Jan 2, 2006")
}
