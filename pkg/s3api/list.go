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
	contToken := q.Get("continuation-token")
	maxKeys := defaultMaxKeys
	if v := q.Get("max-keys"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			maxKeys = n
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()

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
