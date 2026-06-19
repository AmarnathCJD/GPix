package bridge

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/amarnathcjd/gogram/telegram"

	"gpix/pkg/disguise"
	"gpix/pkg/gpmc"
)

func (b *Bot) handleUpload(m *telegram.NewMessage) error {
	if !m.IsReply() {
		_, err := m.Reply("Reply to a photo, video, or file with /upload [saver|quota]")
		return err
	}
	parent, err := m.GetReplyMessage()
	if err != nil {
		_, _ = m.Reply("Could not fetch the replied message: " + err.Error())
		return err
	}
	if !parent.IsMedia() {
		_, err := m.Reply("The replied message has no downloadable media")
		return err
	}

	quality := parseUploadArg(m.Text())
	passphrase := parseKeyValueArg(m.Text(), "encrypt")
	if passphrase == "" {
		passphrase = os.Getenv("GPIX_ENC_PASSPHRASE")
	}

	release, err := b.xfer.Acquire(b.ctx)
	if err != nil {
		_, _ = m.Reply("Busy: " + err.Error())
		return err
	}
	defer release()

	status, err := m.Reply("Preparing…")
	if err != nil {
		return err
	}

	f, cleanup, err := b.xfer.Temp("gpix-up-")
	if err != nil {
		_, _ = status.Edit("Temp file failed: " + err.Error())
		return err
	}
	defer cleanup()
	f.Close()

	throttle := newEditThrottle(status, 5*time.Second)
	throttle.Force("Downloading from Telegram…")
	pm := telegram.NewProgressManager(5).SetMessage(status)
	dlPath, err := parent.Download(&telegram.DownloadOptions{
		FileName:        f.Name(),
		Threads:         4,
		ProgressManager: pm,
	})
	if err != nil {
		throttle.Force("Telegram download failed: " + err.Error())
		return err
	}

	uploadPath := dlPath
	wasDisguised := false
	declaredName := filepath.Base(dlPath)
	if doc := parent.Document(); doc != nil {
		for _, a := range doc.Attributes {
			if fn, ok := a.(*telegram.DocumentAttributeFilename); ok && fn.FileName != "" {
				declaredName = fn.FileName
				break
			}
		}
	}
	commitName := declaredName
	wasEncrypted := false
	if head, err := readHead(dlPath, 512); err == nil && disguise.ShouldWrap("", declaredName, head) {
		pw := ""
		if parseKeyValueArg(m.Text(), "encrypt") != "" || os.Getenv("GPIX_ENC_PASSPHRASE") != "" {
			pw = passphrase
		}
		if pw != "" {
			throttle.Force("Encrypting + wrapping " + declaredName + " as MP4…")
		} else {
			throttle.Force("Wrapping " + declaredName + " as MP4…")
		}
		wrappedPath, err := wrapTGFile(b.xfer.TempDir, dlPath, declaredName, pw)
		if err != nil {
			throttle.Force("Wrap failed: " + err.Error())
			return err
		}
		defer os.Remove(wrappedPath)
		uploadPath = wrappedPath
		commitName = declaredName + ".mp4"
		wasDisguised = true
		wasEncrypted = pw != ""
	}

	throttle.Force("Uploading to Google Photos…")
	res, err := b.gp.UploadFileWithProgress(b.ctx, uploadPath, gpmc.UploadOpts{Quality: quality, OverrideName: commitName}, func(ev gpmc.UploadEvent) {
		switch ev.Stage {
		case gpmc.StageHash:
			throttle.Tick("Hashing… " + fmtPct(ev.BytesDone, ev.BytesTotal))
		case gpmc.StageDedup:
			throttle.Tick("Checking if already uploaded…")
		case gpmc.StageGetToken:
			throttle.Tick("Requesting upload slot…")
		case gpmc.StagePut:
			throttle.Tick("Uploading to Google… " + fmtPct(ev.BytesDone, ev.BytesTotal))
		case gpmc.StageCommit:
			throttle.Tick("Finalizing…")
		}
	})
	if err != nil {
		throttle.Force("Google Photos upload failed: " + err.Error())
		return err
	}

	msg := FormatUploadResult(res)
	if wasDisguised {
		tag := "Disguised as MP4"
		if wasEncrypted {
			tag = "Disguised + encrypted as MP4"
		}
		msg = tag + " · " + declaredName + "\n" + msg
	}
	throttle.Force(msg)
	return nil
}

func (b *Bot) handleGet(m *telegram.NewMessage) error {
	key := parseGetArg(m.Text())
	if key == "" {
		_, err := m.Reply("Usage: /get <media_key>")
		return err
	}

	release, err := b.xfer.Acquire(b.ctx)
	if err != nil {
		_, _ = m.Reply("Busy: " + err.Error())
		return err
	}
	defer release()

	status, err := m.Reply("Resolving download URL…")
	if err != nil {
		return err
	}
	throttle := newEditThrottle(status, 5*time.Second)

	orig, _, err := b.gp.GetDownloadURL(b.ctx, key)
	if err != nil {
		throttle.Force("Resolve failed: " + err.Error())
		return err
	}
	if orig == "" {
		throttle.Force("Server returned no download URL for that key.")
		return fmt.Errorf("empty download URL for %q", key)
	}

	f, cleanup, err := b.xfer.Temp("gpix-get-")
	if err != nil {
		throttle.Force("Temp file failed: " + err.Error())
		return err
	}
	defer cleanup()

	throttle.Force("Downloading from Google…")
	mime, err := httpGetToFileWithProgress(b.ctx, b.gp.HTTPClient(), orig, f, func(done, total int64) {
		throttle.Tick("Downloading from Google… " + fmtPct(done, total))
	})
	_ = f.Close()
	if err != nil {
		throttle.Force("Download failed: " + err.Error())
		return err
	}

	sendPath := f.Name()
	sendName := suggestFileName(orig, mime)
	sendMime := mime
	if head, err := readHead(sendPath, 8192); err == nil && disguise.LooksDisguised(head) {
		pw := parseKeyValueArg(m.Text(), "decrypt")
		if pw == "" {
			pw = os.Getenv("GPIX_ENC_PASSPHRASE")
		}
		if disguise.LooksEncrypted(head) {
			if pw == "" {
				throttle.Force("File is encrypted. Run again as /get " + key + " decrypt:<passphrase> or set GPIX_ENC_PASSPHRASE.")
				return nil
			}
			throttle.Force("Decrypting + unwrapping disguised file…")
		} else {
			throttle.Force("Unwrapping disguised file…")
		}
		extractedPath, origName, err := unwrapTGFile(b.xfer.TempDir, sendPath, pw)
		if err != nil {
			throttle.Force("Unwrap failed: " + err.Error())
			return err
		}
		defer os.Remove(extractedPath)
		sendPath = extractedPath
		sendName = origName
		sendMime = disguise.MIMEForFilename(origName)
	}

	throttle.Force("Sending to Telegram…")
	mode := ResolveSendMode(sendMime)
	pm := telegram.NewProgressManager(5).SetMessage(status)
	opts := &telegram.MediaOptions{
		Caption:       "key: " + key,
		MimeType:      sendMime,
		FileName:      sendName,
		ForceDocument: mode == SendAsDocument,
		Upload: &telegram.UploadOptions{
			Threads:         4,
			ProgressManager: pm,
		},
	}
	if mode == SendAsVideo {
		opts.Attributes = []telegram.DocumentAttribute{
			&telegram.DocumentAttributeVideo{},
		}
	}
	if _, err := b.tg.SendMedia(m.ChatID(), sendPath, opts); err != nil {
		throttle.Force("Send failed: " + err.Error())
		return err
	}
	throttle.Force("Done.")
	return nil
}

func (b *Bot) handleInfo(m *telegram.NewMessage) error {
	email := emailFromAuth(b.gp)
	prof := b.gp.DeviceProfile()
	msg := fmt.Sprintf(
		"gpix bot\nGP account: %s\nDevice: %s (API %d)\nConcurrency: %d",
		email, prof.Model, prof.AndroidAPILevel, cap(b.xfer.Sem),
	)
	_, err := m.Reply(msg)
	return err
}

func (b *Bot) handleList(m *telegram.NewMessage) error {
	n := 20
	if v := parseGetArg(m.Text()); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil && parsed > 0 && parsed <= 100 {
			n = parsed
		}
	}

	status, err := m.Reply("Fetching library…")
	if err != nil {
		return err
	}

	items, err := b.gp.ListRecent(b.ctx, n)
	if err != nil {
		_, _ = status.Edit("List failed: " + err.Error())
		return err
	}
	if len(items) == 0 {
		_, _ = status.Edit("Library is empty (or response had no items).")
		return nil
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "Recent %d items:\n", len(items))
	for i, it := range items {
		kind := "·"
		switch it.Kind {
		case gpmc.KindPhoto:
			kind = "📷"
		case gpmc.KindVideo:
			kind = "🎬"
		}
		name := it.Filename
		if name == "" {
			name = "(no name)"
		}
		size := ""
		if it.SizeBytes > 0 {
			size = " " + fmtBytes(it.SizeBytes)
		}
		when := ""
		if !it.Mtime.IsZero() {
			when = " " + it.Mtime.Format("2006-01-02")
		}
		fmt.Fprintf(&sb, "%d. %s %s%s%s\n   /get %s\n", i+1, kind, name, when, size, it.MediaKey)
	}
	if _, err := status.Edit(sb.String()); err != nil {
		return err
	}
	return nil
}

func httpGetToFile(ctx context.Context, hc *http.Client, urlStr string, w io.Writer) (mime string, err error) {
	return httpGetToFileWithProgress(ctx, hc, urlStr, w, nil)
}

func httpGetToFileWithProgress(ctx context.Context, hc *http.Client, urlStr string, w io.Writer, progress func(done, total int64)) (mime string, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, urlStr, nil)
	if err != nil {
		return "", err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("googleusercontent GET: status %d", resp.StatusCode)
	}
	total := resp.ContentLength
	var reader io.Reader = resp.Body
	if progress != nil {
		reader = gpmc.NewProgressReader(io.NopCloser(resp.Body), total, func(done int64) {
			progress(done, total)
		})
	}
	if _, err := io.Copy(w, reader); err != nil {
		return "", err
	}
	return resp.Header.Get("Content-Type"), nil
}

func suggestFileName(rawURL, mime string) string {
	u, err := url.Parse(rawURL)
	if err == nil {
		base := path.Base(u.Path)
		if base != "" && base != "/" && base != "." {
			return base
		}
	}
	return "media" + extForMime(mime)
}

func extForMime(mime string) string {
	m := strings.ToLower(strings.TrimSpace(mime))
	if i := strings.IndexByte(m, ';'); i >= 0 {
		m = strings.TrimSpace(m[:i])
	}
	switch m {
	case "image/jpeg":
		return ".jpg"
	case "image/png":
		return ".png"
	case "image/webp":
		return ".webp"
	case "image/gif":
		return ".gif"
	case "image/heic":
		return ".heic"
	case "video/mp4":
		return ".mp4"
	case "video/quicktime":
		return ".mov"
	case "video/webm":
		return ".webm"
	}
	return ""
}
