package gpmc

import (
	"context"
	"sync"
	"time"
)

// Album records and per-item album membership are buried inside the same
// library-state RPC we already use for ListPage. The relevant fields, derived
// from Python gpmc.db_update_parser (commented stubs at lines 198-211 and call
// sites 257-262) and decode_message output trees:
//
//   response.1   = LibStateResponse.Body (already decoded as resp.GetBody())
//   response.1.2[]    = media items (already consumed)
//   response.1.3[]    = album/collection records (was disabled in Python)
//   response.1.12[]   = envelopes (shared-album invites — not used here)
//   response.1.9[]    = deletions (not used here)
//   response.1.6      = sync_token
//
// Album record layout (response.1.3[] each entry):
//   1               = album_media_key  (string)
//   2.5             = title            (string)
//   2.7             = total_items      (int)
//   2.8             = type             (int)
//   2.10.6.1        = start_ts_ms      (int)
//   2.10.7.1        = end_ts_ms        (int)
//   2.10.10         = last_activity_ms (int)
//   2.17.1          = cover_item_media_key (string)
//   4.2.3           = album_id         (string)
//   19.1            = sort_order       (int)
//   19.2            = is_custom_ordered (int; 1 = true)
//
// Per-media-item membership:
//   response.1.2[i].2.1.1  = collection_id (string) — the album_media_key the
//                            item belongs to. Empty when the item belongs to
//                            no user album.
//
// The proto stubs the project ships only expose field 2 (media items) and
// field 6 (sync_token) on LibStateResponse.Body; fields 3 and 12 are silently
// dropped during the proto decode. We re-decode the raw response bytes using
// the raw-proto helpers below to recover them.

type rawSyncSnapshot struct {
	syncToken   string
	albums      []Album
	memberships map[string][]string
	itemsByKey  map[string]MediaItem
}

type syncCache struct {
	mu    sync.Mutex
	at    time.Time
	snap  *rawSyncSnapshot
}

const syncCacheTTL = 60 * time.Second

func (c *Client) getSync(ctx context.Context) (*rawSyncSnapshot, error) {
	c.syncMu.Lock()
	if c.sync != nil && time.Since(c.sync.at) < syncCacheTTL {
		s := c.sync.snap
		c.syncMu.Unlock()
		return s, nil
	}
	c.syncMu.Unlock()

	snap, err := c.fetchSync(ctx)
	if err != nil {
		return nil, err
	}

	c.syncMu.Lock()
	c.sync = &syncCache{at: time.Now(), snap: snap}
	c.syncMu.Unlock()
	return snap, nil
}

func (c *Client) fetchSync(ctx context.Context) (*rawSyncSnapshot, error) {
	snap := &rawSyncSnapshot{
		memberships: map[string][]string{},
		itemsByKey:  map[string]MediaItem{},
	}
	cursor := ""
	for i := 0; i < maxAlbumScanPages; i++ {
		body, err := buildLibStateRequest(cursor)
		if err != nil {
			return snap, err
		}
		respBytes, err := c.doProto(ctx, "lib-state", endpointLibState, body, true, c.language)
		if err != nil {
			return snap, err
		}

		bodyBytes, ok := findFieldBytes(respBytes, 1)
		if !ok {
			break
		}

		for _, rec := range findAllFields(bodyBytes, 3) {
			a := parseAlbumRecord(rec)
			if a.MediaKey != "" {
				snap.albums = append(snap.albums, a)
			}
		}

		for _, item := range findAllFields(bodyBytes, 2) {
			mediaKey, _ := findFieldString(item, 1)
			if mediaKey == "" {
				continue
			}
			if metaBytes, ok := findFieldBytes(item, 2); ok {
				if outerColl, ok := findFieldBytes(metaBytes, 1); ok {
					if collKey, ok := findFieldString(outerColl, 1); ok && collKey != "" {
						snap.memberships[collKey] = append(snap.memberships[collKey], mediaKey)
					}
				}
			}
		}

		next, _ := findFieldString(bodyBytes, 6)
		if next == "" || next == cursor {
			snap.syncToken = next
			break
		}
		cursor = next
		snap.syncToken = next
	}

	return snap, nil
}

func parseAlbumRecord(buf []byte) Album {
	a := Album{Source: AlbumSourceUpstream}
	a.MediaKey, _ = findFieldString(buf, 1)
	if meta, ok := findFieldBytes(buf, 2); ok {
		a.Title, _ = findFieldString(meta, 5)
		if v, ok := findFieldVarint(meta, 7); ok {
			a.ItemCount = int(v)
		}
		if v, ok := findFieldVarint(meta, 8); ok {
			a.Type = int(v)
		}
		if range10, ok := findFieldBytes(meta, 10); ok {
			if start, ok := findFieldBytes(range10, 6); ok {
				if v, ok := findFieldVarint(start, 1); ok {
					a.StartMs = int64(v)
				}
			}
			if end, ok := findFieldBytes(range10, 7); ok {
				if v, ok := findFieldVarint(end, 1); ok {
					a.EndMs = int64(v)
				}
			}
			if v, ok := findFieldVarint(range10, 10); ok {
				a.LastActMs = int64(v)
			}
		}
		if cover, ok := findFieldBytes(meta, 17); ok {
			a.CoverMediaKey, _ = findFieldString(cover, 1)
		}
	}
	if ids, ok := findFieldBytes(buf, 4); ok {
		if sub, ok := findFieldBytes(ids, 2); ok {
			a.AlbumID, _ = findFieldString(sub, 3)
		}
	}
	if sort, ok := findFieldBytes(buf, 19); ok {
		if v, ok := findFieldVarint(sort, 1); ok {
			a.SortOrder = int(v)
		}
		if v, ok := findFieldVarint(sort, 2); ok {
			a.IsCustom = v == 1
		}
	}
	return a
}

func (c *Client) walkAlbumsFromSync(ctx context.Context) ([]Album, error) {
	snap, err := c.getSync(ctx)
	if err != nil {
		return nil, err
	}
	return snap.albums, nil
}

func (c *Client) walkAlbumMembershipFromSync(ctx context.Context, albumKey string) ([]string, []MediaItem, error) {
	snap, err := c.getSync(ctx)
	if err != nil {
		return nil, nil, err
	}
	keys := snap.memberships[albumKey]
	items := make([]MediaItem, 0, len(keys))
	for _, k := range keys {
		if it, ok := snap.itemsByKey[k]; ok {
			items = append(items, it)
		}
	}
	return keys, items, nil
}
