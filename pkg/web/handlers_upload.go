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
	passphrase := os.Getenv("GPIX_ENC_PASSPHRASE")
	encryptOn := false

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
		case "encrypt":
			b, _ := io.ReadAll(part)
			if v := string(b); v == "on" || v == "1" || v == "true" {
				encryptOn = true
			}
		case "passphrase":
			b, _ := io.ReadAll(part)
			if v := string(b); v != "" {
				passphrase = v
			}
		case "files":
			fn := part.FileName()
			if fn == "" {
				continue
			}
			pw := ""
			if encryptOn {
				if passphrase == "" {
					results = append(results, fn+"\trefused: encrypt checked but no passphrase (form fields out of order?)")
					_, _ = io.Copy(io.Discard, part)
					continue
				}
				pw = passphrase
			}
			results = append(results, s.uploadOne(r, part, fn, quality, uploadID, pw))
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

func (s *Server) uploadOne(r *http.Request, src io.Reader, name string, quality gpmc.Quality, uploadID, passphrase string) string {
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
	if head, err := readHead(tmpPath, 512); err == nil && disguise.ShouldWrap("", name, head) {
		wrappedPath, err := wrapToTemp(s.cfg.TempDir, tmpPath, name, passphrase)
		if err != nil {
			return name + "\twrap: " + err.Error()
		}
		defer os.Remove(wrappedPath)
		uploadPath = wrappedPath
		commitName = name + ".mp4"
		wasDisguised = true
		wasEncrypted = passphrase != ""
		if wasEncrypted {
			s.progressBus.Publish(uploadID, "encrypted + wrapped "+name+" as mp4")
		} else {
			s.progressBus.Publish(uploadID, "wrapped "+name+" as mp4")
		}
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
		if wasEncrypted {
			marker = "uploaded (disguised, encrypted)"
		} else {
			marker = "uploaded (disguised)"
		}
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

func wrapToTemp(tempDir, srcPath, name, passphrase string) (string, error) {
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
	var wrapped io.Reader
	if passphrase != "" {
		wrapped, _, err = disguise.WrapEncrypted(name, src, passphrase)
		if err != nil {
			os.Remove(out.Name())
			return "", err
		}
	} else {
		wrapped, _ = disguise.Wrap(name, src, st.Size())
	}
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
