package web

import (
	"fmt"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gpix/pkg/gpmc"
)

func (s *Server) handleBrowse(w http.ResponseWriter, r *http.Request) {
	cursor := r.URL.Query().Get("cursor")
	ctx, cancel := withTimeout(r.Context(), 30*time.Second)
	defer cancel()

	page, err := s.gp.ListPage(ctx, cursor)
	if err != nil {
		s.log.Error("list page", "err", err)
		s.render(w, "error", pageData{
			User:    userFromCtx(r.Context()),
			Title:   "could not load library",
			Message: err.Error(),
		})
		return
	}

	items := make([]listingItem, 0, len(page.Items))
	for _, it := range page.Items {
		display, class, disguised := classifyItem(it.Filename, it.Kind)
		displayKind := ""
		switch class {
		case classPhoto:
			displayKind = "Photo"
			if disguised {
				displayKind = "Photo · encrypted"
			}
		case classVideo:
			displayKind = "Video"
			if disguised {
				displayKind = "Video · encrypted"
			}
		case classDoc:
			displayKind = describeKindForFilename(display)
		default:
			displayKind = describeKindForFilename(display)
		}
		items = append(items, listingItem{
			MediaKey:    it.MediaKey,
			Filename:    it.Filename,
			DisplayName: display,
			Kind:        int(it.Kind),
			Class:       string(class),
			IsDisguised: class == classFile,
			DisplayKind: displayKind,
			SizeBytes:   it.SizeBytes,
			Mtime:       it.Mtime,
		})
	}

	s.render(w, "browse", pageData{
		User:       userFromCtx(r.Context()),
		Items:      items,
		NextCursor: page.NextToken,
	})
}

func (s *Server) handleView(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	if key == "" {
		http.NotFound(w, r)
		return
	}

	streamTTL := time.Duration(s.cfg.StreamTokenTTLMin) * time.Minute
	rawTok := s.signMedia(key, streamTTL)
	streamTok := s.signMedia(key, streamTTL)

	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	absStream := scheme + "://" + r.Host + "/stream/" + streamTok

	var kind gpmc.MediaKind
	filename := key
	var sizeBytes int64
	var mtime time.Time

	ctx, cancel := withTimeout(r.Context(), 15*time.Second)
	defer cancel()
	page, err := s.gp.ListPage(ctx, "")
	if err == nil {
		for _, it := range page.Items {
			if it.MediaKey == key {
				kind = it.Kind
				filename = it.Filename
				sizeBytes = it.SizeBytes
				mtime = it.Mtime
				break
			}
		}
	}

	display, class, disguised := classifyItem(filename, kind)
	encrypted := disguised && class != classFile
	displayKind := ""
	switch class {
	case classPhoto:
		displayKind = "Photo"
	case classVideo:
		displayKind = "Video"
	default:
		displayKind = describeKindForFilename(display)
	}

	// HLS adaptive streaming is only available for normal (non-encrypted) videos;
	// encrypted videos are played directly from the decrypting /raw endpoint.
	var qualities []qualityChoice
	if class == classVideo && !encrypted {
		if manifest, mErr := s.gp.GetStreamManifest(ctx, key, "hls"); mErr == nil {
			variants := ParseMasterPlaylist(manifest)
			for _, v := range variants {
				q := qualityChoice{
					Index:     v.Index,
					Label:     qualityLabel(v),
					Width:     v.Width,
					Height:    v.Height,
					Bandwidth: v.Bandwidth,
					StreamURL: fmt.Sprintf("/stream/%s?level=%d", streamTok, v.Index),
					AbsStreamURL: fmt.Sprintf("%s://%s/stream/%s?level=%d",
						scheme, r.Host, streamTok, v.Index),
				}
				qualities = append(qualities, q)
			}
			sort.Slice(qualities, func(i, j int) bool {
				return qualities[i].Bandwidth > qualities[j].Bandwidth
			})
		}
	}

	s.render(w, "view", pageData{
		User:         userFromCtx(r.Context()),
		MediaKey:     key,
		Filename:     display,
		IsVideo:      class == classVideo,
		IsDisguised:  class == classFile,
		MediaClass:   string(class),
		Encrypted:    encrypted,
		OriginalName: display,
		DisplayKind:  displayKind,
		Mtime:        mtime,
		SizeBytes:    sizeBytes,
		StreamURL:    "/stream/" + streamTok,
		RawToken:     rawTok,
		AbsStreamURL: absStream,
		Qualities:    qualities,
		HasQualities: len(qualities) > 0,
	})
}

func describeKindForFilename(name string) string {
	ext := strings.ToLower(filepath.Ext(name))
	if ext == "" {
		return "File"
	}
	switch ext {
	case ".pdf":
		return "PDF document"
	case ".zip", ".7z", ".rar", ".tar", ".gz", ".bz2", ".xz":
		return "Archive"
	case ".exe", ".msi":
		return "Windows executable"
	case ".dmg":
		return "macOS disk image"
	case ".iso":
		return "Disk image"
	case ".apk":
		return "Android package"
	case ".deb", ".rpm":
		return "Linux package"
	case ".doc", ".docx":
		return "Word document"
	case ".xls", ".xlsx":
		return "Spreadsheet"
	case ".ppt", ".pptx":
		return "Presentation"
	case ".txt", ".md", ".log", ".rst":
		return "Text"
	case ".json", ".yaml", ".yml", ".toml", ".ini", ".conf", ".cfg":
		return "Config"
	case ".go", ".rs", ".py", ".js", ".ts", ".c", ".cpp", ".h", ".java", ".sh", ".bat", ".ps1":
		return "Source"
	case ".mp3", ".wav", ".flac", ".m4a", ".ogg":
		return "Audio"
	case ".bin", ".dat":
		return "Binary"
	}
	return strings.ToUpper(strings.TrimPrefix(ext, ".")) + " file"
}

func qualityLabel(v HLSVariant) string {
	if v.Height > 0 {
		return fmt.Sprintf("%dp", v.Height)
	}
	if v.Bandwidth > 0 {
		return fmt.Sprintf("%d kbps", v.Bandwidth/1000)
	}
	return fmt.Sprintf("level %d", v.Index)
}
