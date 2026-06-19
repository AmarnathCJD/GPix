package web

import (
	"bufio"
	"strconv"
	"strings"
)

type HLSVariant struct {
	Index       int
	Bandwidth   int
	Width       int
	Height      int
	FrameRate   string
	Codecs      string
	PlaylistURL string
}

func ParseMasterPlaylist(s string) []HLSVariant {
	var out []HLSVariant
	sc := bufio.NewScanner(strings.NewReader(s))
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	idx := 0
	var pending HLSVariant
	pendingValid := false
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#EXT-X-STREAM-INF:") {
			pending = HLSVariant{Index: idx}
			parseAttrs(strings.TrimPrefix(line, "#EXT-X-STREAM-INF:"), &pending)
			pendingValid = true
			continue
		}
		if strings.HasPrefix(line, "#") {
			continue
		}
		if pendingValid {
			pending.PlaylistURL = line
			out = append(out, pending)
			idx++
			pendingValid = false
		}
	}
	return out
}

func parseAttrs(s string, v *HLSVariant) {
	for _, attr := range splitAttrs(s) {
		before, after, ok := strings.Cut(attr, "=")
		if !ok {
			continue
		}
		k := strings.TrimSpace(before)
		val := strings.Trim(strings.TrimSpace(after), `"`)
		switch k {
		case "BANDWIDTH":
			v.Bandwidth, _ = strconv.Atoi(val)
		case "RESOLUTION":
			if x := strings.IndexByte(val, 'x'); x > 0 {
				v.Width, _ = strconv.Atoi(val[:x])
				v.Height, _ = strconv.Atoi(val[x+1:])
			}
		case "FRAME-RATE":
			v.FrameRate = val
		case "CODECS":
			v.Codecs = val
		}
	}
}

func splitAttrs(s string) []string {
	var out []string
	var cur strings.Builder
	inQ := false
	for _, r := range s {
		switch {
		case r == '"':
			inQ = !inQ
			cur.WriteRune(r)
		case r == ',' && !inQ:
			out = append(out, cur.String())
			cur.Reset()
		default:
			cur.WriteRune(r)
		}
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}
