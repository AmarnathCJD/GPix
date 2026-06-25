package web

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	// Register the GIF decoder (JPEG and PNG decoders come from their imports above).
	_ "image/gif"

	"gpix/pkg/disguise"
	"gpix/pkg/mediacrypt"
)

const thumbCacheMaxBytes = 40 << 20 // don't try to thumbnail decrypted images larger than this

// readCloser ties a logical reader to the underlying closer.
type webReadCloser struct {
	r io.Reader
	c io.Closer
}

func (rc webReadCloser) Read(p []byte) (int, error) { return rc.r.Read(p) }
func (rc webReadCloser) Close() error               { return rc.c.Close() }

type webMultiCloser struct {
	r       io.Reader
	closers []io.Closer
}

func (m webMultiCloser) Read(p []byte) (int, error) { return m.r.Read(p) }
func (m webMultiCloser) Close() error {
	var first error
	for _, c := range m.closers {
		if err := c.Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// openDecrypted opens the true original bytes for a media key: it resolves the
// download URL, un-disguises and decrypts as needed. The caller closes it.
func (s *Server) openDecrypted(ctx context.Context, mediaKey string) (io.ReadCloser, string, error) {
	url, err := s.urlCache.Get(ctx, mediaKey)
	if err != nil {
		return nil, "", err
	}
	resp, _, err := s.doProxyGet(ctx, url, mediaKey, false)
	if err != nil {
		return nil, "", err
	}
	if resp.StatusCode == 403 || resp.StatusCode == 410 {
		resp.Body.Close()
		s.urlCache.Invalidate(mediaKey)
		if url, err = s.urlCache.Get(ctx, mediaKey); err != nil {
			return nil, "", err
		}
		if resp, _, err = s.doProxyGet(ctx, url, mediaKey, true); err != nil {
			return nil, "", err
		}
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, "", fmt.Errorf("openDecrypted: upstream %d", resp.StatusCode)
	}

	ct := resp.Header.Get("Content-Type")
	br := bufio.NewReaderSize(resp.Body, 64*1024)
	head, _ := br.Peek(8192)
	if disguise.LooksDisguised(head) {
		hdr, payload, derr := disguise.Extract(br)
		if derr == nil {
			ct = disguiseMIME(hdr.Filename)
			if s.crypt != nil {
				pbr := bufio.NewReader(payload)
				if ph, _ := pbr.Peek(len(mediacrypt.Magic)); mediacrypt.HasMagic(ph) {
					eh, dr, eerr := s.crypt.DecryptingReader(pbr)
					if eerr != nil {
						resp.Body.Close()
						return nil, "", eerr
					}
					return webMultiCloser{r: dr, closers: []io.Closer{dr, resp.Body}}, disguiseMIME(eh.Name), nil
				}
				return webReadCloser{r: pbr, c: resp.Body}, ct, nil
			}
			return webReadCloser{r: payload, c: resp.Body}, ct, nil
		}
	}
	return webReadCloser{r: br, c: resp.Body}, ct, nil
}

// thumbCachePath returns the on-disk path for a generated thumbnail.
func (s *Server) thumbCachePath(mediaKey string, size int, ext string) string {
	sum := sha256.Sum256([]byte(mediaKey))
	return filepath.Join(s.thumbDir, fmt.Sprintf("%s_%d.%s", hex.EncodeToString(sum[:16]), size, ext))
}

// thumbKindMeta maps a kind ("photo"|"doc") to its cache extension + MIME type.
func thumbKindMeta(kind string) (ext, contentType string) {
	if kind == "doc" {
		return "png", "image/png"
	}
	return "jpg", "image/jpeg"
}

// cachedThumbFile returns a previously generated thumbnail from disk, if present.
func (s *Server) cachedThumbFile(mediaKey string, size int, kind string) ([]byte, string, bool) {
	if s.thumbDir == "" {
		return nil, "", false
	}
	ext, ct := thumbKindMeta(kind)
	if data, err := os.ReadFile(s.thumbCachePath(mediaKey, size, ext)); err == nil {
		return data, ct, true
	}
	return nil, "", false
}

// thumbBytes returns the generated thumbnail for a disguised photo/doc, using
// the on-disk cache. It decrypts the original, which is why callers must keep it
// OFF the hot grid path (see queueThumb) — only the single-item view generates
// synchronously. kind is "photo" (JPEG) or "doc" (PNG).
func (s *Server) thumbBytes(ctx context.Context, mediaKey, name string, size int, kind string) ([]byte, string, error) {
	ext, ct := thumbKindMeta(kind)
	path := s.thumbCachePath(mediaKey, size, ext)
	if data, err := os.ReadFile(path); err == nil {
		return data, ct, nil
	}
	unlock := s.thumbLock(path)
	defer unlock()
	if data, err := os.ReadFile(path); err == nil { // generated while we waited
		return data, ct, nil
	}

	rc, _, err := s.openDecrypted(ctx, mediaKey)
	if err != nil {
		return nil, "", err
	}
	defer rc.Close()

	bw := &byteWriter{}
	if kind == "doc" {
		content, rerr := io.ReadAll(io.LimitReader(rc, 512<<10)) // first 512 KB
		if rerr != nil {
			return nil, "", rerr
		}
		width, maxLines := docRenderParams(size)
		if err := png.Encode(bw, renderDocImage(content, width, maxLines)); err != nil {
			return nil, "", err
		}
	} else {
		img, _, derr := image.Decode(io.LimitReader(rc, thumbCacheMaxBytes))
		if derr != nil {
			return nil, "", derr
		}
		if err := jpeg.Encode(bw, downscale(img, size), &jpeg.Options{Quality: 82}); err != nil {
			return nil, "", err
		}
	}
	_ = os.WriteFile(path, bw.b, 0o600) // best-effort cache write
	return bw.b, ct, nil
}

// queueThumb generates a thumbnail in the background (bounded concurrency) so a
// grid request never blocks on a decrypt+download. If the worker pool is full it
// simply skips — the next grid load will try again.
func (s *Server) queueThumb(mediaKey, name string, size int, kind string) {
	if s.thumbSem == nil {
		return
	}
	select {
	case s.thumbSem <- struct{}{}:
	default:
		return // at capacity; try again next time
	}
	go func() {
		defer func() { <-s.thumbSem }()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if _, _, err := s.thumbBytes(ctx, mediaKey, name, size, kind); err != nil {
			s.log.Debug("background thumb", "key", mediaKey, "err", err)
		}
	}()
}

// docRenderParams maps a requested thumbnail size to a render width and line cap:
// small sizes are grid previews, large sizes are the full-page document view.
func docRenderParams(size int) (width, maxLines int) {
	if size >= 1024 {
		return 1000, 600
	}
	return 560, 40
}

func writeThumb(w http.ResponseWriter, data []byte, contentType string) {
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "private, max-age=86400")
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(data)))
	_, _ = w.Write(data)
}

// thumbLock returns an unlock func, serialising generation per cache path.
func (s *Server) thumbLock(key string) func() {
	mu, _ := s.thumbLocks.LoadOrStore(key, &sync.Mutex{})
	m := mu.(*sync.Mutex)
	m.Lock()
	return m.Unlock
}

type byteWriter struct{ b []byte }

func (bw *byteWriter) Write(p []byte) (int, error) {
	bw.b = append(bw.b, p...)
	return len(p), nil
}

// downscale resizes src to fit within maxDim on its longest side using box
// averaging (good quality for downscaling, pure stdlib).
func downscale(src image.Image, maxDim int) image.Image {
	b := src.Bounds()
	sw, sh := b.Dx(), b.Dy()
	if sw == 0 || sh == 0 {
		return src
	}
	scale := 1.0
	if sw > sh {
		if sw > maxDim {
			scale = float64(maxDim) / float64(sw)
		}
	} else {
		if sh > maxDim {
			scale = float64(maxDim) / float64(sh)
		}
	}
	dw, dh := int(float64(sw)*scale), int(float64(sh)*scale)
	if dw < 1 {
		dw = 1
	}
	if dh < 1 {
		dh = 1
	}
	if dw == sw && dh == sh {
		return src
	}

	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	xRatio := float64(sw) / float64(dw)
	yRatio := float64(sh) / float64(dh)
	for dy := 0; dy < dh; dy++ {
		sy0 := b.Min.Y + int(float64(dy)*yRatio)
		sy1 := b.Min.Y + int(float64(dy+1)*yRatio)
		if sy1 <= sy0 {
			sy1 = sy0 + 1
		}
		for dx := 0; dx < dw; dx++ {
			sx0 := b.Min.X + int(float64(dx)*xRatio)
			sx1 := b.Min.X + int(float64(dx+1)*xRatio)
			if sx1 <= sx0 {
				sx1 = sx0 + 1
			}
			var rs, gs, bs, as, n uint64
			for sy := sy0; sy < sy1; sy++ {
				for sx := sx0; sx < sx1; sx++ {
					rr, gg, bb, aa := src.At(sx, sy).RGBA()
					rs += uint64(rr)
					gs += uint64(gg)
					bs += uint64(bb)
					as += uint64(aa)
					n++
				}
			}
			if n == 0 {
				n = 1
			}
			off := dst.PixOffset(dx, dy)
			dst.Pix[off+0] = uint8((rs / n) >> 8)
			dst.Pix[off+1] = uint8((gs / n) >> 8)
			dst.Pix[off+2] = uint8((bs / n) >> 8)
			dst.Pix[off+3] = uint8((as / n) >> 8)
		}
	}
	return dst
}
