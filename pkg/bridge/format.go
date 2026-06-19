package bridge

import (
	"fmt"
	"net/url"
	"strings"

	"gpix/pkg/gpmc"
)

func emailFromAuth(c *gpmc.Client) string {
	parsed, err := url.ParseQuery(c.AuthData())
	if err != nil {
		return "?"
	}
	if e := parsed.Get("Email"); e != "" {
		return e
	}
	return "?"
}

func FormatUploadResult(r gpmc.UploadResult) string {
	verb := "Uploaded"
	if r.Skipped {
		verb = "Already in library"
	}
	return fmt.Sprintf("%s: %s\nhttps://photos.google.com/lc/%s", verb, r.MediaKey, r.MediaKey)
}

func parseKeyValueArg(text, key string) string {
	prefix := key + ":"
	for _, tok := range strings.Fields(text) {
		if strings.HasPrefix(tok, prefix) {
			return strings.TrimPrefix(tok, prefix)
		}
	}
	return ""
}

func parseUploadArg(text string) gpmc.Quality {
	switch {
	case containsToken(text, "saver"):
		return gpmc.QualitySaver
	case containsToken(text, "quota"):
		return gpmc.QualityUseQuota
	default:
		return gpmc.QualityOriginal
	}
}

func parseGetArg(text string) string {
	for i, r := range text {
		if r == ' ' || r == '\t' {
			rest := text[i+1:]
			for j, r2 := range rest {
				if r2 != ' ' && r2 != '\t' {
					return firstToken(rest[j:])
				}
			}
		}
	}
	return ""
}

func containsToken(text, want string) bool {
	for {
		start := -1
		for i, r := range text {
			if r != ' ' && r != '\t' {
				start = i
				break
			}
		}
		if start < 0 {
			return false
		}
		end := len(text)
		for i := start; i < len(text); i++ {
			if text[i] == ' ' || text[i] == '\t' {
				end = i
				break
			}
		}
		if text[start:end] == want {
			return true
		}
		text = text[end:]
	}
}

func firstToken(s string) string {
	end := len(s)
	for i, r := range s {
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' {
			end = i
			break
		}
	}
	return s[:end]
}
