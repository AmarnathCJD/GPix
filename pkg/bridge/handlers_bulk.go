package bridge

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/amarnathcjd/gogram/telegram"

	"gpix/pkg/disguise"
	"gpix/pkg/gpmc"
)

func (b *Bot) handleCatchMedia(m *telegram.NewMessage) error {
	if !m.IsMedia() {
		return nil
	}
	t := strings.TrimSpace(m.Text())
	if strings.HasPrefix(t, "/") {
		return nil
	}
	name := tgMediaName(m)
	b.queue.Add(queuedItem{
		ChatID:    m.ChatID(),
		MessageID: m.ID,
		Name:      name,
	})
	n := b.queue.Len()
	_, _ = m.Reply(fmt.Sprintf("Queued %s · %d in queue. Send /uploadall to upload.", name, n))
	return nil
}

func (b *Bot) handleQueue(m *telegram.NewMessage) error {
	snap := b.queue.Snapshot()
	if len(snap) == 0 {
		_, err := m.Reply("Queue is empty. Forward photos / videos / files to me, then /uploadall.")
		return err
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "Queue: %d item(s)\n", len(snap))
	for i, it := range snap {
		fmt.Fprintf(&sb, "%d. %s\n", i+1, it.Name)
	}
	sb.WriteString("\nSend /uploadall [N] [encrypt:pw] to drain.")
	_, err := m.Reply(sb.String())
	return err
}

func (b *Bot) handleClearQueue(m *telegram.NewMessage) error {
	n := b.queue.Clear()
	_, err := m.Reply(fmt.Sprintf("Cleared %d item(s) from queue.", n))
	return err
}

func (b *Bot) handleUploadAll(m *telegram.NewMessage) error {
	if b.queue.Len() == 0 {
		_, err := m.Reply("Queue is empty. Forward photos / videos / files to me first.")
		return err
	}

	limit := 0
	for _, tok := range strings.Fields(m.Text()) {
		if n, err := strconv.Atoi(tok); err == nil && n > 0 {
			limit = n
			break
		}
	}
	quality := parseUploadArg(m.Text())
	passphrase := parseKeyValueArg(m.Text(), "encrypt")
	if passphrase == "" {
		passphrase = os.Getenv("GPIX_ENC_PASSPHRASE")
	}

	items := b.queue.DrainN(limit)
	status, err := m.Reply(fmt.Sprintf("Uploading %d item(s)…", len(items)))
	if err != nil {
		return err
	}

	results := make([]string, 0, len(items))
	canceled := false
	for i, it := range items {
		if b.ctx.Err() != nil {
			results = append(results, fmt.Sprintf("• %s — canceled", it.Name))
			canceled = true
			break
		}
		header := fmt.Sprintf("[%d/%d] %s\n\n", i+1, len(items), it.Name)
		_, _ = status.Edit(header + strings.Join(results, "\n"))

		parent, err := b.fetchMessageByID(b.ctx, it.ChatID, it.MessageID)
		if err != nil {
			results = append(results, fmt.Sprintf("• %s — fetch: %s", it.Name, err.Error()))
			continue
		}
		line, err := b.uploadOneFromTG(b.ctx, parent, quality, passphrase, func(s string) {
			_, _ = status.Edit(header + s + "\n\n" + strings.Join(results, "\n"))
		})
		if err != nil {
			results = append(results, fmt.Sprintf("• %s — error: %s", it.Name, err.Error()))
			continue
		}
		results = append(results, "• "+line)
	}
	_ = canceled

	left := b.queue.Len()
	final := fmt.Sprintf("Done · %d uploaded", len(results))
	if left > 0 {
		final += fmt.Sprintf(" · %d still queued", left)
	}
	final += "\n\n" + strings.Join(results, "\n")
	_, _ = status.Edit(final)
	return nil
}

func (b *Bot) uploadOneFromTG(ctx context.Context, parent *telegram.NewMessage, quality gpmc.Quality, passphrase string, progress func(string)) (string, error) {
	release, err := b.xfer.Acquire(ctx)
	if err != nil {
		return "", err
	}
	defer release()

	f, cleanup, err := b.xfer.Temp("gpix-bulk-")
	if err != nil {
		return "", err
	}
	defer cleanup()
	f.Close()

	declaredName := tgMediaName(parent)
	progress("Downloading: " + declaredName)
	dlPath, err := parent.Download(&telegram.DownloadOptions{
		FileName: f.Name(),
		Threads:  4,
	})
	if err != nil {
		return "", fmt.Errorf("tg download: %w", err)
	}

	uploadPath := dlPath
	commitName := declaredName
	wasDisguised := false
	wasEncrypted := false
	if head, err := readHead(dlPath, 512); err == nil && disguise.ShouldWrap("", declaredName, head) {
		if passphrase != "" {
			progress("Encrypting + wrapping: " + declaredName)
		} else {
			progress("Wrapping: " + declaredName)
		}
		wrappedPath, werr := wrapTGFile(b.xfer.TempDir, dlPath, declaredName, passphrase)
		if werr != nil {
			return "", fmt.Errorf("wrap: %w", werr)
		}
		defer os.Remove(wrappedPath)
		uploadPath = wrappedPath
		commitName = declaredName + ".mp4"
		wasDisguised = true
		wasEncrypted = passphrase != ""
	}

	progress("Uploading to GP: " + declaredName)
	res, err := b.gp.UploadFile(ctx, uploadPath, gpmc.UploadOpts{Quality: quality, OverrideName: commitName})
	if err != nil {
		return "", fmt.Errorf("gp upload: %w", err)
	}

	verb := "uploaded"
	if res.Skipped {
		verb = "already in library"
	}
	if wasDisguised {
		if wasEncrypted {
			verb = "encrypted + disguised"
		} else {
			verb = "disguised"
		}
		if res.Skipped {
			verb = "already in library"
		}
	}
	return fmt.Sprintf("%s · %s · %s", declaredName, verb, res.MediaKey), nil
}

func (b *Bot) fetchMessageByID(ctx context.Context, chatID int64, msgID int32) (*telegram.NewMessage, error) {
	_ = ctx
	msgs, err := b.tg.GetMessages(chatID, &telegram.SearchOption{
		IDs: &telegram.InputMessageID{ID: msgID},
	})
	if err != nil {
		return nil, err
	}
	if len(msgs) == 0 {
		return nil, fmt.Errorf("message %d not found", msgID)
	}
	return &msgs[0], nil
}

func tgMediaName(m *telegram.NewMessage) string {
	if doc := m.Document(); doc != nil {
		for _, a := range doc.Attributes {
			if fn, ok := a.(*telegram.DocumentAttributeFilename); ok && fn.FileName != "" {
				return fn.FileName
			}
		}
		return fmt.Sprintf("document-%d", doc.ID)
	}
	if ph := m.Photo(); ph != nil {
		return fmt.Sprintf("photo-%d.jpg", ph.ID)
	}
	return fmt.Sprintf("media-%d", m.ID)
}

var _ = filepath.Base
var _ = time.Second
