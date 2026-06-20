package webdav

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/webdav"

	"gpix/pkg/disguise"
	"gpix/pkg/gpmc"
	"gpix/pkg/gpmc/albumstore"
)

type gpixFS struct {
	gp    *gpmc.Client
	cfg   Config
	cache *fileCache
}

func newFS(gp *gpmc.Client, cfg Config) *gpixFS {
	return &gpixFS{gp: gp, cfg: cfg, cache: newFileCache()}
}

func (f *gpixFS) Mkdir(ctx context.Context, name string, perm os.FileMode) error {
	parts := splitParts(name)
	if len(parts) == 2 && parts[0] == "albums" {
		title, err := url.PathUnescape(parts[1])
		if err != nil {
			title = parts[1]
		}
		if _, ok, _ := f.gp.FindAlbumByTitle(ctx, title); ok {
			return os.ErrExist
		}
		key, err := f.gp.CreateAlbum(ctx, title, nil)
		if err != nil {
			return err
		}
		if store := f.gp.AlbumStore(); store != nil {
			_ = store.PutAlbum(ctx, albumstore.Album{
				MediaKey:  key,
				Title:     title,
				CreatedAt: time.Now(),
			})
		}
		return nil
	}
	return webdav.ErrForbidden
}

func (f *gpixFS) Rename(ctx context.Context, oldName, newName string) error {
	return webdav.ErrForbidden
}

func cleanPath(name string) string {
	name = strings.TrimSuffix(path.Clean("/"+strings.TrimSpace(name)), "/")
	if name == "" {
		return "/"
	}
	return name
}

func splitParts(name string) []string {
	name = cleanPath(name)
	if name == "/" {
		return nil
	}
	return strings.Split(strings.TrimPrefix(name, "/"), "/")
}

type dirKind int

const (
	dirRoot dirKind = iota
	dirLibrary
	dirPage
	dirAlbums
	dirAlbum
	dirInvalid
)

type resolved struct {
	kind     dirKind
	cursor   string // page cursor for a dirPage
	label    string // directory base name
	isFile   bool
	item     gpmc.MediaItem
	albumKey string
	album    gpmc.Album
}

func (f *gpixFS) resolve(ctx context.Context, name string) (resolved, error) {
	parts := splitParts(name)
	switch len(parts) {
	case 0:
		return resolved{kind: dirRoot, label: ""}, nil
	case 1:
		switch parts[0] {
		case "library":
			return resolved{kind: dirLibrary, label: "library"}, nil
		case "albums":
			return resolved{kind: dirAlbums, label: "albums"}, nil
		}
		return resolved{kind: dirInvalid}, os.ErrNotExist
	case 2:
		if parts[0] == "library" {
			cursor, ok := f.cursorForLabel(ctx, parts[1])
			if !ok {
				return resolved{kind: dirInvalid}, os.ErrNotExist
			}
			return resolved{kind: dirPage, cursor: cursor, label: parts[1]}, nil
		}
		if parts[0] == "albums" {
			title := unescapeSegment(parts[1])
			album, ok, err := f.gp.FindAlbumByTitle(ctx, title)
			if err != nil {
				return resolved{}, err
			}
			if !ok {
				return resolved{kind: dirInvalid}, os.ErrNotExist
			}
			return resolved{kind: dirAlbum, label: parts[1], albumKey: album.MediaKey, album: album}, nil
		}
		return resolved{kind: dirInvalid}, os.ErrNotExist
	case 3:
		if parts[0] == "library" {
			cursor, ok := f.cursorForLabel(ctx, parts[1])
			if !ok {
				return resolved{kind: dirInvalid}, os.ErrNotExist
			}
			page, err := f.gp.ListPage(ctx, cursor)
			if err != nil {
				return resolved{}, err
			}
			for _, it := range page.Items {
				if displayName(it) == parts[2] {
					return resolved{isFile: true, label: parts[2], item: it}, nil
				}
			}
			return resolved{kind: dirInvalid}, os.ErrNotExist
		}
		if parts[0] == "albums" {
			title := unescapeSegment(parts[1])
			album, ok, err := f.gp.FindAlbumByTitle(ctx, title)
			if err != nil {
				return resolved{}, err
			}
			if !ok {
				return resolved{kind: dirInvalid}, os.ErrNotExist
			}
			items, err := f.gp.ListAlbumItems(ctx, album.MediaKey)
			if err != nil {
				return resolved{}, err
			}
			for _, it := range items {
				if displayName(it) == parts[2] {
					return resolved{isFile: true, label: parts[2], item: it, albumKey: album.MediaKey, album: album}, nil
				}
			}
			return resolved{kind: dirInvalid}, os.ErrNotExist
		}
		return resolved{kind: dirInvalid}, os.ErrNotExist
	default:
		return resolved{kind: dirInvalid}, os.ErrNotExist
	}
}

func unescapeSegment(s string) string {
	if u, err := url.PathUnescape(s); err == nil {
		return u
	}
	return s
}

// cursorForLabel maps "recent" → "" (first page) and "page-N" → the cursor
// reached by walking N-1 pages from the start.
func (f *gpixFS) cursorForLabel(ctx context.Context, label string) (string, bool) {
	if label == "recent" || label == "page-1" {
		return "", true
	}
	if !strings.HasPrefix(label, "page-") {
		return "", false
	}
	n, err := strconv.Atoi(strings.TrimPrefix(label, "page-"))
	if err != nil || n < 1 {
		return "", false
	}
	cursor := ""
	for i := 1; i < n; i++ {
		page, err := f.gp.ListPage(ctx, cursor)
		if err != nil || page.NextToken == "" {
			return "", false
		}
		cursor = page.NextToken
	}
	return cursor, true
}

func (f *gpixFS) Stat(ctx context.Context, name string) (os.FileInfo, error) {
	r, err := f.resolve(ctx, name)
	if err != nil {
		return nil, err
	}
	if r.isFile {
		return fileInfo(r.item), nil
	}
	return dirInfo(path.Base(cleanPath(name))), nil
}

func (f *gpixFS) RemoveAll(ctx context.Context, name string) error {
	parts := splitParts(name)
	if len(parts) == 2 && parts[0] == "albums" {
		title := unescapeSegment(parts[1])
		album, ok, err := f.gp.FindAlbumByTitle(ctx, title)
		if err != nil {
			return err
		}
		if !ok {
			return os.ErrNotExist
		}
		if store := f.gp.AlbumStore(); store != nil {
			return store.DeleteAlbum(ctx, album.MediaKey)
		}
		return webdav.ErrForbidden
	}
	if len(parts) == 3 && parts[0] == "albums" {
		r, err := f.resolve(ctx, name)
		if err != nil {
			return err
		}
		if !r.isFile {
			return webdav.ErrForbidden
		}
		if store := f.gp.AlbumStore(); store != nil {
			return store.RemoveMembers(ctx, r.albumKey, []string{r.item.MediaKey})
		}
		return webdav.ErrForbidden
	}

	r, err := f.resolve(ctx, name)
	if err != nil {
		return err
	}
	if !r.isFile {
		return webdav.ErrForbidden
	}
	res, err := f.gp.DeleteByMediaKeys(ctx, []string{r.item.MediaKey}, false)
	if err != nil {
		return err
	}
	if e := res[r.item.MediaKey]; e != nil {
		return e
	}
	return nil
}

func (f *gpixFS) OpenFile(ctx context.Context, name string, flag int, perm os.FileMode) (webdav.File, error) {
	parts := splitParts(name)

	if flag&(os.O_WRONLY|os.O_RDWR|os.O_CREATE) != 0 {
		if len(parts) == 3 && parts[0] == "albums" {
			title := unescapeSegment(parts[1])
			album, ok, err := f.gp.FindAlbumByTitle(ctx, title)
			if err != nil {
				return nil, err
			}
			if !ok {
				return nil, os.ErrNotExist
			}
			return newPendingUpload(f, name, album.MediaKey)
		}
		return newPendingUpload(f, name, "")
	}

	r, err := f.resolve(ctx, name)
	if err != nil {
		return nil, err
	}
	if !r.isFile {
		return &dir{fs: f, name: cleanPath(name)}, nil
	}
	return &readFile{fs: f, item: r.item}, nil
}

type dir struct {
	fs   *gpixFS
	name string
}

func (d *dir) Close() error                   { return nil }
func (d *dir) Read([]byte) (int, error)       { return 0, errIsDir }
func (d *dir) Write([]byte) (int, error)      { return 0, errIsDir }
func (d *dir) Seek(int64, int) (int64, error) { return 0, errIsDir }
func (d *dir) Stat() (fs.FileInfo, error)     { return dirInfo(path.Base(d.name)), nil }

var errIsDir = errors.New("webdav: is a directory")

func (d *dir) Readdir(count int) ([]fs.FileInfo, error) {
	ctx := context.Background()
	parts := splitParts(d.name)
	var infos []fs.FileInfo
	switch len(parts) {
	case 0:
		infos = []fs.FileInfo{dirInfo("library"), dirInfo("albums")}
	case 1:
		switch parts[0] {
		case "library":
			labels, err := d.fs.pageLabels(ctx)
			if err != nil {
				return nil, err
			}
			for _, l := range labels {
				infos = append(infos, dirInfo(l))
			}
		case "albums":
			albums, err := d.fs.gp.ListAlbums(ctx)
			if err != nil {
				return nil, err
			}
			seen := map[string]bool{}
			for _, a := range albums {
				name := albumDirName(a)
				if name == "" || seen[name] {
					continue
				}
				seen[name] = true
				infos = append(infos, dirInfoAt(name, a.ModTime()))
			}
		default:
			return nil, os.ErrNotExist
		}
	case 2:
		if parts[0] == "library" {
			cursor, ok := d.fs.cursorForLabel(ctx, parts[1])
			if !ok {
				return nil, os.ErrNotExist
			}
			page, err := d.fs.gp.ListPage(ctx, cursor)
			if err != nil {
				return nil, err
			}
			seen := make(map[string]bool, len(page.Items))
			for _, it := range page.Items {
				n := displayName(it)
				if seen[n] {
					continue
				}
				seen[n] = true
				infos = append(infos, fileInfo(it))
			}
		} else if parts[0] == "albums" {
			title := unescapeSegment(parts[1])
			album, ok, err := d.fs.gp.FindAlbumByTitle(ctx, title)
			if err != nil {
				return nil, err
			}
			if !ok {
				return nil, os.ErrNotExist
			}
			items, err := d.fs.gp.ListAlbumItems(ctx, album.MediaKey)
			if err != nil {
				return nil, err
			}
			seen := make(map[string]bool, len(items))
			for _, it := range items {
				n := displayName(it)
				if seen[n] {
					continue
				}
				seen[n] = true
				infos = append(infos, fileInfo(it))
			}
		} else {
			return nil, os.ErrNotExist
		}
	default:
		return nil, os.ErrNotExist
	}
	if count > 0 && count < len(infos) {
		infos = infos[:count]
	}
	return infos, nil
}

// pageLabels walks the cursor chain once to produce recent/page-2/page-3/...
// capped so a huge library doesn't make a PROPFIND of /library unbounded.
func (f *gpixFS) pageLabels(ctx context.Context) ([]string, error) {
	labels := []string{"recent"}
	cursor := ""
	for i := 2; i <= maxPages; i++ {
		page, err := f.gp.ListPage(ctx, cursor)
		if err != nil {
			return nil, err
		}
		if page.NextToken == "" {
			break
		}
		labels = append(labels, "page-"+strconv.Itoa(i))
		cursor = page.NextToken
	}
	return labels, nil
}

const maxPages = 50

type readFile struct {
	fs   *gpixFS
	item gpmc.MediaItem

	mu     sync.Mutex
	src    io.ReadSeekCloser
	offset int64
}

func (rf *readFile) ensure() error {
	if rf.src != nil {
		return nil
	}
	src, err := rf.fs.openMedia(context.Background(), rf.item)
	if err != nil {
		return err
	}
	rf.src = src
	if rf.offset != 0 {
		if _, err := rf.src.Seek(rf.offset, io.SeekStart); err != nil {
			return err
		}
	}
	return nil
}

func (rf *readFile) Read(p []byte) (int, error) {
	rf.mu.Lock()
	defer rf.mu.Unlock()
	if err := rf.ensure(); err != nil {
		return 0, err
	}
	n, err := rf.src.Read(p)
	rf.offset += int64(n)
	return n, err
}

func (rf *readFile) Seek(offset int64, whence int) (int64, error) {
	rf.mu.Lock()
	defer rf.mu.Unlock()
	if rf.src == nil {
		switch whence {
		case io.SeekStart:
			rf.offset = offset
		case io.SeekCurrent:
			rf.offset += offset
		case io.SeekEnd:
			rf.offset = rf.item.SizeBytes + offset
		}
		return rf.offset, nil
	}
	n, err := rf.src.Seek(offset, whence)
	if err == nil {
		rf.offset = n
	}
	return n, err
}

func (rf *readFile) Close() error {
	rf.mu.Lock()
	defer rf.mu.Unlock()
	if rf.src != nil {
		err := rf.src.Close()
		rf.src = nil
		return err
	}
	return nil
}

func (rf *readFile) Write([]byte) (int, error)          { return 0, os.ErrPermission }
func (rf *readFile) Readdir(int) ([]fs.FileInfo, error) { return nil, errors.New("not a directory") }
func (rf *readFile) Stat() (fs.FileInfo, error)         { return fileInfo(rf.item), nil }

// openMedia resolves the download URL and returns a seekable reader. Disguised
// payloads are extracted into a tempfile (extraction is not seekable in place),
// otherwise a Range-backed reader streams directly from googleusercontent.
func (f *gpixFS) openMedia(ctx context.Context, item gpmc.MediaItem) (io.ReadSeekCloser, error) {
	if p, ok := f.cache.get(item.MediaKey); ok {
		file, err := os.Open(p)
		if err == nil {
			return file, nil
		}
	}

	orig, _, err := f.gp.GetDownloadURL(ctx, item.MediaKey)
	if err != nil {
		return nil, err
	}
	if orig == "" {
		return nil, fmt.Errorf("webdav: no download url for %s", item.MediaKey)
	}

	head, supportsRange, err := f.probe(ctx, orig)
	if err != nil {
		return nil, err
	}

	if disguise.LooksDisguised(head) {
		return f.extractToTemp(ctx, item, orig)
	}
	if !supportsRange {
		return f.downloadToTemp(ctx, item, orig)
	}
	return &rangeReader{
		ctx:  ctx,
		hc:   f.gp.HTTPClient(),
		url:  orig,
		size: item.SizeBytes,
	}, nil
}

func (f *gpixFS) probe(ctx context.Context, url string) (head []byte, supportsRange bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, false, err
	}
	req.Header.Set("Range", "bytes=0-65535")
	resp, err := f.gp.HTTPClient().Do(req)
	if err != nil {
		return nil, false, err
	}
	defer resp.Body.Close()
	supportsRange = resp.StatusCode == http.StatusPartialContent
	buf, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	return buf, supportsRange, nil
}

func (f *gpixFS) extractToTemp(ctx context.Context, item gpmc.MediaItem, url string) (io.ReadSeekCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := f.gp.HTTPClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	_, payload, err := disguise.Extract(resp.Body)
	if errors.Is(err, disguise.ErrEncrypted) {
		resp.Body.Close()
		req2, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		resp2, err2 := f.gp.HTTPClient().Do(req2)
		if err2 != nil {
			return nil, err2
		}
		defer resp2.Body.Close()
		_, payload, err = disguise.ExtractWithPassphrase(resp2.Body, f.cfg.EncPassphrase)
	}
	if err != nil {
		return nil, err
	}
	return f.spool(item, payload)
}

func (f *gpixFS) downloadToTemp(ctx context.Context, item gpmc.MediaItem, url string) (io.ReadSeekCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := f.gp.HTTPClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return f.spool(item, resp.Body)
}

func (f *gpixFS) spool(item gpmc.MediaItem, r io.Reader) (io.ReadSeekCloser, error) {
	tmp, err := os.CreateTemp(os.TempDir(), "gpix-dav-*")
	if err != nil {
		return nil, err
	}
	if _, err := io.Copy(tmp, r); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return nil, err
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return nil, err
	}
	f.cache.put(item.MediaKey, tmp.Name())
	return tmp, nil
}

// rangeReader presents a remote URL as a seekable stream by issuing a new
// ranged GET whenever the read offset jumps. Sequential reads reuse one body.
type rangeReader struct {
	ctx  context.Context
	hc   *http.Client
	url  string
	size int64

	pos  int64
	body io.ReadCloser
	bpos int64 // offset the current body started at
}

func (r *rangeReader) Read(p []byte) (int, error) {
	if r.pos >= r.size && r.size > 0 {
		return 0, io.EOF
	}
	if r.body == nil || r.bpos != r.pos {
		if err := r.openAt(r.pos); err != nil {
			return 0, err
		}
	}
	n, err := r.body.Read(p)
	r.pos += int64(n)
	r.bpos += int64(n)
	return n, err
}

func (r *rangeReader) openAt(off int64) error {
	if r.body != nil {
		r.body.Close()
		r.body = nil
	}
	req, err := http.NewRequestWithContext(r.ctx, http.MethodGet, r.url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Range", "bytes="+strconv.FormatInt(off, 10)+"-")
	resp, err := r.hc.Do(req)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusPartialContent && resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return fmt.Errorf("webdav: range get status %d", resp.StatusCode)
	}
	r.body = resp.Body
	r.bpos = off
	return nil
}

func (r *rangeReader) Seek(offset int64, whence int) (int64, error) {
	var abs int64
	switch whence {
	case io.SeekStart:
		abs = offset
	case io.SeekCurrent:
		abs = r.pos + offset
	case io.SeekEnd:
		abs = r.size + offset
	default:
		return 0, errors.New("webdav: invalid whence")
	}
	if abs < 0 {
		return 0, errors.New("webdav: negative seek")
	}
	r.pos = abs
	return abs, nil
}

func (r *rangeReader) Close() error {
	if r.body != nil {
		err := r.body.Close()
		r.body = nil
		return err
	}
	return nil
}

type pendingUpload struct {
	fs        *gpixFS
	name      string
	albumKey  string
	tmp       *os.File
	done      bool
}

func newPendingUpload(f *gpixFS, name, albumKey string) (*pendingUpload, error) {
	tmp, err := os.CreateTemp(os.TempDir(), "gpix-davup-*")
	if err != nil {
		return nil, err
	}
	return &pendingUpload{fs: f, name: name, albumKey: albumKey, tmp: tmp}, nil
}

func (p *pendingUpload) Write(b []byte) (int, error) { return p.tmp.Write(b) }

func (p *pendingUpload) Close() error {
	if p.done {
		return nil
	}
	p.done = true
	tmpPath := p.tmp.Name()
	defer os.Remove(tmpPath)
	if err := p.tmp.Close(); err != nil {
		return err
	}

	filename := path.Base(cleanPath(p.name))
	ctx := context.Background()
	res, err := p.fs.gp.UploadFile(ctx, tmpPath, gpmc.UploadOpts{
		OverrideName:      filename,
		EncryptPassphrase: p.fs.cfg.EncPassphrase,
	})
	if err != nil {
		return err
	}

	if p.albumKey != "" && res.MediaKey != "" {
		if err := p.fs.gp.AddMediaToAlbum(ctx, p.albumKey, []string{res.MediaKey}); err != nil {
			return fmt.Errorf("webdav: add to album: %w", err)
		}
		if store := p.fs.gp.AlbumStore(); store != nil {
			_ = store.AddMembers(ctx, p.albumKey, []string{res.MediaKey})
		}
	}
	return nil
}

func (p *pendingUpload) Read([]byte) (int, error)       { return 0, os.ErrPermission }
func (p *pendingUpload) Seek(int64, int) (int64, error) { return 0, os.ErrPermission }
func (p *pendingUpload) Readdir(int) ([]fs.FileInfo, error) {
	return nil, errors.New("not a directory")
}
func (p *pendingUpload) Stat() (fs.FileInfo, error) {
	return &staticInfo{name: path.Base(cleanPath(p.name)), mode: 0o644, modTime: time.Now()}, nil
}

func displayName(it gpmc.MediaItem) string {
	if it.Filename != "" {
		return it.Filename
	}
	return it.MediaKey
}

func albumDirName(a gpmc.Album) string {
	t := strings.TrimSpace(a.Title)
	if t == "" {
		return ""
	}
	return strings.ReplaceAll(t, "/", "-")
}

func fileInfo(it gpmc.MediaItem) fs.FileInfo {
	return &staticInfo{
		name:    displayName(it),
		size:    it.SizeBytes,
		mode:    0o644,
		modTime: it.Mtime,
	}
}

func dirInfo(name string) fs.FileInfo {
	return &staticInfo{name: name, mode: fs.ModeDir | 0o755, modTime: time.Now(), isDir: true}
}

func dirInfoAt(name string, mt time.Time) fs.FileInfo {
	if mt.IsZero() {
		mt = time.Now()
	}
	return &staticInfo{name: name, mode: fs.ModeDir | 0o755, modTime: mt, isDir: true}
}

type staticInfo struct {
	name    string
	size    int64
	mode    fs.FileMode
	modTime time.Time
	isDir   bool
}

func (s *staticInfo) Name() string       { return s.name }
func (s *staticInfo) Size() int64        { return s.size }
func (s *staticInfo) Mode() fs.FileMode  { return s.mode }
func (s *staticInfo) ModTime() time.Time { return s.modTime }
func (s *staticInfo) IsDir() bool        { return s.isDir }
func (s *staticInfo) Sys() any           { return nil }
