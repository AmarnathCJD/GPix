package s3api

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const defaultMaxKeys = 1000

func (s *Server) listBuckets(w http.ResponseWriter, r *http.Request) {
	res := listAllMyBucketsResult{
		XMLNS: s3XMLNS,
		Owner: canonicalUser{ID: "gpix", DisplayName: "gpix"},
		Buckets: bucketList{Bucket: []bucketEntry{{
			Name:         s.cfg.Bucket,
			CreationDate: isoDate(time.Unix(0, 0)),
		}}},
	}
	writeXML(w, http.StatusOK, res)
}

func (s *Server) listObjectsV2(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	prefix := q.Get("prefix")
	delimiter := q.Get("delimiter")
	contToken := q.Get("continuation-token")
	maxKeys := defaultMaxKeys
	if v := q.Get("max-keys"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			maxKeys = n
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()

	if strings.HasPrefix(prefix, albumPrefix) || prefix == strings.TrimSuffix(albumPrefix, "/") {
		s.listAlbumsAsObjects(ctx, w, prefix, delimiter, maxKeys)
		return
	}
	if delimiter == "/" && prefix == "" {
		s.listRootWithAlbumCommonPrefix(ctx, w, maxKeys)
		return
	}

	res := listObjectsV2Result{
		XMLNS:    s3XMLNS,
		Name:     s.cfg.Bucket,
		Prefix:   prefix,
		MaxKeys:  maxKeys,
		Contents: []objectEntry{},
	}
	if contToken != "" {
		res.ContinuationToken = contToken
	}

	cursor := contToken
	collected := 0
	for collected < maxKeys {
		page, err := s.gp.ListPage(ctx, cursor)
		if err != nil {
			writeError(w, r, errInternalError)
			return
		}
		for _, it := range page.Items {
			if prefix != "" && !strings.HasPrefix(it.MediaKey, prefix) {
				continue
			}
			if collected >= maxKeys {
				break
			}
			res.Contents = append(res.Contents, objectEntry{
				Key:          it.MediaKey,
				LastModified: isoDate(it.Mtime),
				ETag:         etagFromSHA1(it.SHA1),
				Size:         it.SizeBytes,
				StorageClass: "STANDARD",
				Owner:        canonicalUser{ID: "gpix", DisplayName: "gpix"},
			})
			collected++
		}
		if page.NextToken == "" {
			cursor = ""
			break
		}
		cursor = page.NextToken
		if collected >= maxKeys {
			break
		}
	}

	res.KeyCount = len(res.Contents)
	if cursor != "" {
		res.IsTruncated = true
		res.NextContinuationToken = cursor
	}
	writeXML(w, http.StatusOK, res)
}

const albumPrefix = "albums/"

func (s *Server) listAlbumsAsObjects(ctx context.Context, w http.ResponseWriter, prefix, delimiter string, maxKeys int) {
	res := listObjectsV2Result{
		XMLNS:    s3XMLNS,
		Name:     s.cfg.Bucket,
		Prefix:   prefix,
		MaxKeys:  maxKeys,
		Contents: []objectEntry{},
	}

	rest := strings.TrimPrefix(prefix, albumPrefix)
	rest = strings.TrimPrefix(rest, strings.TrimSuffix(albumPrefix, "/"))

	if rest == "" || !strings.Contains(rest, "/") {
		albums, err := s.gp.ListAlbums(ctx)
		if err != nil {
			writeError(w, nil, errInternalError)
			return
		}
		seen := map[string]bool{}
		for _, a := range albums {
			name := strings.TrimSpace(a.Title)
			if name == "" || seen[name] {
				continue
			}
			seen[name] = true
			cp := albumPrefix + name + "/"
			res.CommonPrefixes = append(res.CommonPrefixes, commonPrefix{Prefix: cp})
		}
		res.KeyCount = len(res.Contents) + len(res.CommonPrefixes)
		writeXML(w, http.StatusOK, res)
		return
	}

	title := strings.TrimSuffix(rest, "/")
	if idx := strings.Index(title, "/"); idx >= 0 {
		title = title[:idx]
	}
	album, ok, err := s.gp.FindAlbumByTitle(ctx, title)
	if err != nil {
		writeError(w, nil, errInternalError)
		return
	}
	if !ok {
		writeXML(w, http.StatusOK, res)
		return
	}
	items, err := s.gp.ListAlbumItems(ctx, album.MediaKey)
	if err != nil {
		writeError(w, nil, errInternalError)
		return
	}
	for _, it := range items {
		if len(res.Contents) >= maxKeys {
			break
		}
		res.Contents = append(res.Contents, objectEntry{
			Key:          albumPrefix + title + "/" + it.MediaKey,
			LastModified: isoDate(it.Mtime),
			ETag:         etagFromSHA1(it.SHA1),
			Size:         it.SizeBytes,
			StorageClass: "STANDARD",
			Owner:        canonicalUser{ID: "gpix", DisplayName: "gpix"},
		})
	}
	res.KeyCount = len(res.Contents)
	writeXML(w, http.StatusOK, res)
}

func (s *Server) listRootWithAlbumCommonPrefix(ctx context.Context, w http.ResponseWriter, maxKeys int) {
	res := listObjectsV2Result{
		XMLNS:          s3XMLNS,
		Name:           s.cfg.Bucket,
		Delimiter:      "/",
		MaxKeys:        maxKeys,
		Contents:       []objectEntry{},
		CommonPrefixes: []commonPrefix{{Prefix: albumPrefix}},
	}

	cursor := ""
	for len(res.Contents) < maxKeys {
		page, err := s.gp.ListPage(ctx, cursor)
		if err != nil {
			writeError(w, nil, errInternalError)
			return
		}
		for _, it := range page.Items {
			if len(res.Contents) >= maxKeys {
				break
			}
			res.Contents = append(res.Contents, objectEntry{
				Key:          it.MediaKey,
				LastModified: isoDate(it.Mtime),
				ETag:         etagFromSHA1(it.SHA1),
				Size:         it.SizeBytes,
				StorageClass: "STANDARD",
				Owner:        canonicalUser{ID: "gpix", DisplayName: "gpix"},
			})
		}
		if page.NextToken == "" {
			break
		}
		cursor = page.NextToken
	}
	res.KeyCount = len(res.Contents) + len(res.CommonPrefixes)
	writeXML(w, http.StatusOK, res)
}
