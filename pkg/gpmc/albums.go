package gpmc

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

var (
	endpointCreateAlbum     = "https://photosdata-pa.googleapis.com/6439526531001121323/8386163679468898444"
	endpointAddMediaToAlbum = "https://photosdata-pa.googleapis.com/6439526531001121323/484917746253879292"
)

type AlbumSource int

const (
	AlbumSourceUpstream AlbumSource = iota
	AlbumSourceLocal
)

func (s AlbumSource) String() string {
	if s == AlbumSourceLocal {
		return "local"
	}
	return "upstream"
}

type Album struct {
	MediaKey      string
	Title         string
	Source        AlbumSource
	ItemCount     int
	CoverMediaKey string
	StartMs       int64
	EndMs         int64
	LastActMs     int64
	Type          int
	SortOrder     int
	IsCustom      bool
	AlbumID       string
}

func (a Album) ModTime() time.Time {
	switch {
	case a.LastActMs > 0:
		return time.UnixMilli(a.LastActMs)
	case a.EndMs > 0:
		return time.UnixMilli(a.EndMs)
	case a.StartMs > 0:
		return time.UnixMilli(a.StartMs)
	}
	return time.Time{}
}

func (c *Client) CreateAlbum(ctx context.Context, title string, mediaKeys []string) (string, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		return "", errors.New("gpmc: empty album title")
	}
	ts := time.Now().Unix()

	body := []byte{}
	body = encString(body, 1, title)
	body = encInt(body, 2, ts)
	body = encInt(body, 3, 1)
	for _, k := range mediaKeys {
		inner := encString(nil, 1, k)
		entry := encSubMessage(nil, 1, inner)
		body = encSubMessage(body, 4, entry)
	}
	body = encSubMessage(body, 6, nil)
	{
		sub := encInt(nil, 1, 3)
		body = encSubMessage(body, 7, sub)
	}
	{
		sub := []byte{}
		sub = encString(sub, 3, c.profile.Model)
		sub = encString(sub, 4, c.profile.Make)
		sub = encInt(sub, 5, int64(c.profile.AndroidAPILevel))
		body = encSubMessage(body, 8, sub)
	}

	respBytes, err := c.doProto(ctx, "create-album", endpointCreateAlbum, body, true, "")
	if err != nil {
		return "", err
	}
	l1, ok := findFieldBytes(respBytes, 1)
	if !ok {
		return "", fmt.Errorf("gpmc create-album: response missing field 1")
	}
	key, ok := findFieldString(l1, 1)
	if !ok || key == "" {
		return "", fmt.Errorf("gpmc create-album: response missing album media_key")
	}
	return key, nil
}

func (c *Client) AddMediaToAlbum(ctx context.Context, albumMediaKey string, mediaKeys []string) error {
	if albumMediaKey == "" {
		return errors.New("gpmc: empty album media_key")
	}
	if len(mediaKeys) == 0 {
		return nil
	}
	body := []byte{}
	for _, k := range mediaKeys {
		body = encString(body, 1, k)
	}
	body = encString(body, 2, albumMediaKey)
	{
		sub := encInt(nil, 1, 2)
		body = encSubMessage(body, 5, sub)
	}
	{
		sub := []byte{}
		sub = encString(sub, 3, c.profile.Model)
		sub = encString(sub, 4, c.profile.Make)
		sub = encInt(sub, 5, int64(c.profile.AndroidAPILevel))
		body = encSubMessage(body, 6, sub)
	}
	body = encInt(body, 7, time.Now().Unix())

	_, err := c.doProto(ctx, "add-to-album", endpointAddMediaToAlbum, body, true, "")
	return err
}

func (c *Client) ListAlbums(ctx context.Context) ([]Album, error) {
	byKey := map[string]Album{}
	byTitle := map[string]string{}

	upstream, _ := c.walkAlbumsFromSync(ctx)
	for _, a := range upstream {
		byKey[a.MediaKey] = a
		if a.Title != "" {
			byTitle[strings.ToLower(a.Title)] = a.MediaKey
		}
	}

	if c.albums != nil {
		local, err := c.albums.ListAlbums(ctx)
		if err == nil {
			for _, la := range local {
				if existing, ok := byKey[la.MediaKey]; ok {
					if la.CoverMediaKey != "" {
						existing.CoverMediaKey = la.CoverMediaKey
					}
					if la.Title != "" {
						existing.Title = la.Title
					}
					byKey[la.MediaKey] = existing
				} else {
					byKey[la.MediaKey] = Album{
						MediaKey:      la.MediaKey,
						Title:         la.Title,
						AlbumID:       la.AlbumID,
						CoverMediaKey: la.CoverMediaKey,
						Source:        AlbumSourceLocal,
					}
				}
			}
		}
	}

	out := make([]Album, 0, len(byKey))
	for _, a := range byKey {
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool {
		ti, tj := out[i].ModTime(), out[j].ModTime()
		if !ti.Equal(tj) {
			return ti.After(tj)
		}
		return strings.ToLower(out[i].Title) < strings.ToLower(out[j].Title)
	})
	return out, nil
}

func (c *Client) FindAlbumByTitle(ctx context.Context, title string) (Album, bool, error) {
	if c.albums != nil {
		if la, ok, err := c.albums.GetAlbumByTitle(ctx, title); err == nil && ok {
			return Album{
				MediaKey:      la.MediaKey,
				Title:         la.Title,
				AlbumID:       la.AlbumID,
				CoverMediaKey: la.CoverMediaKey,
				Source:        AlbumSourceLocal,
			}, true, nil
		}
	}
	albums, err := c.ListAlbums(ctx)
	if err != nil {
		return Album{}, false, err
	}
	want := strings.ToLower(strings.TrimSpace(title))
	for _, a := range albums {
		if strings.ToLower(a.Title) == want {
			return a, true, nil
		}
	}
	return Album{}, false, nil
}

func (c *Client) GetAlbumByKey(ctx context.Context, albumKey string) (Album, bool, error) {
	if c.albums != nil {
		if la, err := c.albums.GetAlbum(ctx, albumKey); err == nil && la.MediaKey != "" {
			return Album{
				MediaKey:      la.MediaKey,
				Title:         la.Title,
				AlbumID:       la.AlbumID,
				CoverMediaKey: la.CoverMediaKey,
				Source:        AlbumSourceLocal,
			}, true, nil
		}
	}
	albums, err := c.ListAlbums(ctx)
	if err != nil {
		return Album{}, false, err
	}
	for _, a := range albums {
		if a.MediaKey == albumKey {
			return a, true, nil
		}
	}
	return Album{}, false, nil
}

func (c *Client) ListAlbumItems(ctx context.Context, albumMediaKey string) ([]MediaItem, error) {
	keys := map[string]bool{}

	if c.albums != nil {
		members, err := c.albums.MembersOf(ctx, albumMediaKey)
		if err == nil {
			for _, k := range members {
				keys[k] = true
			}
		}
	}

	libKeys, libItems, err := c.walkAlbumMembershipFromSync(ctx, albumMediaKey)
	if err == nil {
		for _, k := range libKeys {
			keys[k] = true
		}
	}

	if len(keys) == 0 {
		return nil, nil
	}

	out := make([]MediaItem, 0, len(keys))
	seen := map[string]bool{}
	for _, it := range libItems {
		if keys[it.MediaKey] && !seen[it.MediaKey] {
			out = append(out, it)
			seen[it.MediaKey] = true
		}
	}

	missing := []string{}
	for k := range keys {
		if !seen[k] {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		extra, err := c.materializeItems(ctx, missing)
		if err == nil {
			for _, it := range extra {
				if !seen[it.MediaKey] {
					out = append(out, it)
					seen[it.MediaKey] = true
				}
			}
		}
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Mtime.After(out[j].Mtime) })
	return out, nil
}

const maxAlbumScanPages = 50

func (c *Client) materializeItems(ctx context.Context, keys []string) ([]MediaItem, error) {
	want := map[string]bool{}
	for _, k := range keys {
		want[k] = true
	}
	out := []MediaItem{}
	cursor := ""
	for i := 0; i < maxAlbumScanPages && len(want) > 0; i++ {
		page, err := c.ListPage(ctx, cursor)
		if err != nil {
			return out, err
		}
		for _, it := range page.Items {
			if want[it.MediaKey] {
				out = append(out, it)
				delete(want, it.MediaKey)
			}
		}
		if page.NextToken == "" {
			break
		}
		cursor = page.NextToken
	}
	return out, nil
}
