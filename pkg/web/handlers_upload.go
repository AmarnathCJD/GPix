package web

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"

	"gpix/pkg/disguise"
	"gpix/pkg/gpmc"
	"gpix/pkg/mediacrypt"
)

const maxUploadBytes = 5 << 30

func (s *Server) handleUploadForm(w http.ResponseWriter, r *http.Request) {
	s.render(w, "upload", pageData{
		User: userFromCtx(r.Context()),
	})
}

func (s *Server) handleUploadSubmit(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadBytes)
	mr, err := r.MultipartReader()
	if err != nil {
		http.Error(w, "multipart: "+err.Error(), 400)
		return
	}

	quality := gpmc.QualityOriginal
	uploadID := randID()

	var results []string

	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			http.Error(w, "part: "+err.Error(), 400)
			return
		}

		switch part.FormName() {
		case "quality":
			b, _ := io.ReadAll(part)
			switch string(b) {
			case "saver":
				quality = gpmc.QualitySaver
			case "quota":
				quality = gpmc.QualityUseQuota
			}
		case "upload_id":
			b, _ := io.ReadAll(part)
			if v := string(b); v != "" {
				uploadID = v
			}
		case "files":
			fn := part.FileName()
			if fn == "" {
				continue
			}
			results = append(results, s.uploadOne(r, part, fn, quality, uploadID))
		case "_csrf":
			_, _ = io.Copy(io.Discard, part)
		}
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if len(results) == 0 {
		_, _ = io.WriteString(w, "no files uploaded\n")
		return
	}
	for _, r := range results {
		_, _ = io.WriteString(w, r+"\n")
	}
	s.progressBus.Publish(uploadID, "done")
}

func (s *Server) uploadOne(r *http.Request, src io.Reader, name string, quality gpmc.Quality, uploadID string) string {
	select {
	case s.tempSemaphore <- struct{}{}:
	case <-r.Context().Done():
		return name + "\tcanceled"
	}
	defer func() { <-s.tempSemaphore }()

	tf, err := os.CreateTemp(s.cfg.TempDir, "gpix-web-*"+filepath.Ext(name))
	if err != nil {
		return name + "\ttemp file: " + err.Error()
	}
	tmpPath := tf.Name()
	defer os.Remove(tmpPath)

	s.progressBus.Publish(uploadID, "receiving "+name+"…")
	if _, err := io.Copy(tf, src); err != nil {
		tf.Close()
		return name + "\trecv: " + err.Error()
	}
	tf.Close()

	uploadPath := tmpPath
	displayName := name
	commitName := name
	wasDisguised := false
	wasEncrypted := false

	if s.crypt != nil && s.crypt.Enabled() {
		// Encrypt the original bytes, then disguise the ciphertext as MP4 so
		// Google Photos accepts it. Google never sees the real media.
		st, serr := os.Stat(tmpPath)
		if serr != nil {
			return name + "\tstat: " + serr.Error()
		}
		s.progressBus.Publish(uploadID, "encrypting "+name+"…")
		encPath, err := encryptToTemp(s.cfg.TempDir, tmpPath, name, st.Size(), s.crypt)
		if err != nil {
			return name + "\tencrypt: " + err.Error()
		}
		defer os.Remove(encPath)
		wrappedPath, err := wrapToTemp(s.cfg.TempDir, encPath, name)
		if err != nil {
			return name + "\twrap: " + err.Error()
		}
		defer os.Remove(wrappedPath)
		uploadPath = wrappedPath
		commitName = name + ".mp4"
		wasDisguised = true
		wasEncrypted = true
	} else if head, err := readHead(tmpPath, 512); err == nil && disguise.ShouldWrap("", name, head) {
		wrappedPath, err := wrapToTemp(s.cfg.TempDir, tmpPath, name)
		if err != nil {
			return name + "\twrap: " + err.Error()
		}
		defer os.Remove(wrappedPath)
		uploadPath = wrappedPath
		commitName = name + ".mp4"
		wasDisguised = true
		s.progressBus.Publish(uploadID, "wrapped "+name+" as mp4")
	}

	s.progressBus.Publish(uploadID, "uploading "+displayName+" to google photos…")
	res, err := s.gp.UploadFileWithProgress(r.Context(), uploadPath, gpmc.UploadOpts{Quality: quality, OverrideName: commitName}, func(ev gpmc.UploadEvent) {
		s.progressBus.Publish(uploadID, fmt.Sprintf("%s · %s · %d/%d", displayName, ev.Stage.String(), ev.BytesDone, ev.BytesTotal))
	})
	if err != nil {
		return displayName + "\terror: " + err.Error()
	}
	marker := "uploaded"
	if wasDisguised {
		marker = "uploaded (disguised)"
	}
	if wasEncrypted {
		marker = "uploaded (encrypted)"
	}
	if res.Skipped {
		marker = "already in library"
	}
	return displayName + "\t" + marker + " · " + res.MediaKey
}

func readHead(path string, n int) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	buf := make([]byte, n)
	read, err := io.ReadFull(f, buf)
	if err == io.ErrUnexpectedEOF || err == io.EOF {
		return buf[:read], nil
	}
	if err != nil {
		return nil, err
	}
	return buf[:read], nil
}

func encryptToTemp(tempDir, srcPath, name string, size int64, crypt *mediacrypt.Manager) (string, error) {
	src, err := os.Open(srcPath)
	if err != nil {
		return "", err
	}
	defer src.Close()
	out, err := os.CreateTemp(tempDir, "gpix-enc-*")
	if err != nil {
		return "", err
	}
	defer out.Close()
	if err := crypt.Encrypt(out, src, size, name); err != nil {
		os.Remove(out.Name())
		return "", err
	}
	return out.Name(), nil
}

func wrapToTemp(tempDir, srcPath, name string) (string, error) {
	src, err := os.Open(srcPath)
	if err != nil {
		return "", err
	}
	defer src.Close()
	st, err := src.Stat()
	if err != nil {
		return "", err
	}
	out, err := os.CreateTemp(tempDir, "gpix-disg-*.mp4")
	if err != nil {
		return "", err
	}
	defer out.Close()
	wrapped, _ := disguise.Wrap(name, src, st.Size())
	if _, err := io.Copy(out, wrapped); err != nil {
		os.Remove(out.Name())
		return "", err
	}
	return out.Name(), nil
}

func (s *Server) handleProgressSSE(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "missing id", 400)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", 500)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	ch := s.progressBus.Subscribe(id)
	defer s.progressBus.Unsubscribe(id)
	flusher.Flush()

	for {
		select {
		case <-r.Context().Done():
			return
		case msg, ok := <-ch:
			if !ok {
				fmt.Fprint(w, "event: done\ndata: {}\n\n")
				flusher.Flush()
				return
			}
			if msg == "done" {
				fmt.Fprint(w, "event: done\ndata: {}\n\n")
				flusher.Flush()
				return
			}
			fmt.Fprintf(w, "data: %s\n\n", msg)
			flusher.Flush()
		}
	}
}

func randID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
