# Testing the WebDAV mount

## What to expect

gpix exposes a read-mostly WebDAV view of your Google Photos library at `http://127.0.0.1:8080/dav/` (loopback only), guarded by HTTP Basic auth with realm `gpix`. The tree is a fixed three-level virtual filesystem (`/dav/` → `library/` → page labels → files), so directory creation and rename are intentionally forbidden. By the end of this runbook you will have proven the four things that matter: **PROPFIND** lists the virtual tree, **GET** streams (and transparently un-disguises) files, **PUT** uploads back into the library, and **DELETE** moves items to Google's trash.

## Setup

WebDAV is gated by two env vars. The current `.env` ships without them, so `mountWebDAV` (`main.go:212-225`) silently logs `webdav disabled` at DEBUG and returns — every request to `/dav/*` then 404s from the web router.

One-time, generate a bcrypt hash (cost 12, same as the web UI):

```bash
go run . -hashpw
# paste password twice, copy the $2a$12$... output
```

Add to `.env` (keep the existing `GP_AUTH_DATA`, `TG_*` lines intact):

```bash
WEBDAV_USERNAME=amarnath
WEBDAV_PASSWORD_HASH='$2a$12$....paste.here....'
# Optional — only needed for encrypted-disguise round-trip tests:
WEBDAV_ENC_PASSPHRASE=some-strong-passphrase
```

**Single-quote the hash. This is load-bearing.** An unquoted line like `WEBDAV_PASSWORD_HASH=$2a$12$abc...` will have `$2a`, `$12`, and `$abc` expanded by the `.env` loader into empty strings, producing a corrupted hash that silently 401s forever. Working example:

```bash
WEBDAV_PASSWORD_HASH='$2a$12$KQK7vF9bN3J5cZQXyR2pWuY6sV8tN1mR4dGfH7jL9oP2qS3tU5vW6'
```

If you saved `.env` from a Windows editor, strip CRs (git-bash does ship `dos2unix`; if yours doesn't, use sed):

```bash
dos2unix .env 2>/dev/null || sed -i 's/\r$//' .env
```

Restart gpix (`go run . -web` or `-all`) and confirm the `webdav mounted` log line. To prove the gating works, comment both vars, restart, watch for `webdav disabled` at DEBUG level, then re-enable and restart again.

```bash
curl -sS -o /dev/null -w '%{http_code}\n' http://127.0.0.1:8080/dav/
# 401 means mounted; 404 means still disabled
```

## TL;DR smoke

> **Smoke test (under a minute) — one curl proves the mount is alive:**

```bash
curl -i -u amarnath:gpix-test-pw-2026 -X OPTIONS http://127.0.0.1:8080/dav/
```

**expect:** `200 OK`, empty body, and these headers verbatim:

- `Allow: OPTIONS, GET, HEAD, PUT, DELETE, MKCOL, PROPFIND, LOCK, UNLOCK`
- `DAV: 1, 2`
- `MS-Author-Via: DAV`

(PROPPATCH, COPY, MOVE work despite not being in `Allow` — see Edge cases.)

## Full curl test sequence

All commands assume `WEBDAV_USERNAME=amarnath` and the test password `gpix-test-pw-2026`. Replace if you used different creds.

**A note on XML formatting.** Git-bash on Windows does not ship `xmllint`. Either install it (`pacman -S libxml2` in MSYS2, `choco install xmlstarlet` then substitute `xmlstarlet fo`), pretty-print with Python (`python -c "import sys,xml.dom.minidom as m; print(m.parseString(sys.stdin.read()).toprettyxml())"`), or just read the raw XML — these responses are short. The runbook below pipes to `head -50` and uses `grep` so it works on a vanilla MSYS2 install.

**A note on MSYS path mangling.** Git-bash rewrites argument-position paths that start with `/` into Windows paths. For URLs containing `/dav/...`, either single-quote the entire URL (as below) or prefix the command with `MSYS_NO_PATHCONV=1`.

**A note on empty PROPFIND bodies.** All PROPFIND requests below send no body. Per RFC 4918 an empty body MUST be treated as `<allprop/>`, and `x/net/webdav` honors that — this is intentional, not an oversight. To send the canonical body explicitly:

```bash
curl ... -H 'Content-Type: application/xml' \
  -d '<?xml version="1.0"?><d:propfind xmlns:d="DAV:"><d:allprop/></d:propfind>'
```

**Pre-flight health check.**

```bash
curl -sS -o /dev/null -w 'HTTP %{http_code}\n' 'http://127.0.0.1:8080/dav/'
# 401 → mount alive. 404 → WEBDAV_* env not loaded; fix .env and restart before continuing.
```

**1. OPTIONS without auth → instant 401**

```bash
time curl -i -X OPTIONS 'http://127.0.0.1:8080/dav/'
```

**expect:** `401 Unauthorized`, `WWW-Authenticate: Basic realm="gpix"`, body is `unauthorized` followed by a newline byte (i.e. `unauthorized\n` where `\n` denotes the literal LF). `real < 0.1s` — the 300 ms penalty only fires on credential mismatch, not on missing Authorization header (`auth.go:14-17` vs `:23-26`).

**2. OPTIONS with auth → 200**

```bash
curl -i -u amarnath:gpix-test-pw-2026 -X OPTIONS 'http://127.0.0.1:8080/dav/'
```

**expect:** see the smoke test above.

**3. PROPFIND Depth: 0 on /dav/ → 207**

```bash
curl -i -u amarnath:gpix-test-pw-2026 -X PROPFIND -H 'Depth: 0' \
  -H 'Content-Type: application/xml' 'http://127.0.0.1:8080/dav/'
```

**expect:** `207 Multi-Status`, one `<response>` for the root collection, containing `<resourcetype><collection/></resourcetype>`. (The DAV namespace prefix is typically `D:` but is not contractual — don't assert on the prefix.)

**4. PROPFIND Depth: 1 on /dav/ → 207, lists library/**

```bash
curl -s -u amarnath:gpix-test-pw-2026 -X PROPFIND -H 'Depth: 1' \
  'http://127.0.0.1:8080/dav/' | grep -oE '<(D:)?href>[^<]+</(D:)?href>'
```

**expect:** exactly two `<response>` blocks (prefix may vary) — `/dav/` and `/dav/library/`. Nothing else (no `foo/`, no test directories).

**5. PROPFIND Depth: 1 on /dav/library/ → 207, lists pages**

```bash
curl -s -u amarnath:gpix-test-pw-2026 -X PROPFIND -H 'Depth: 1' \
  'http://127.0.0.1:8080/dav/library/' | grep -oE '<(D:)?href>[^<]+</(D:)?href>'
```

**expect:** `recent/`, then `page-2/`, `page-3/`, … capped at **50** entries total (`maxPages` in `fs.go:260`). Each is a `<collection/>`.

**6. PROPFIND Depth: 1 on /dav/library/recent/ → 207, files with sizes**

```bash
curl -s -u amarnath:gpix-test-pw-2026 -X PROPFIND -H 'Depth: 1' \
  'http://127.0.0.1:8080/dav/library/recent/' | head -80
```

**expect:** one `<response>` per media item, each with `<getcontentlength>`, `<getlastmodified>`, `<getcontenttype>`. Filename comes from `gpmc.MediaItem.Filename` (raw — including spaces, unicode, disguise cover extensions like `.jpg`).

**7. HEAD on a file → headers only**

```bash
FNAME=$(curl -s -u amarnath:gpix-test-pw-2026 -X PROPFIND -H 'Depth: 1' \
  'http://127.0.0.1:8080/dav/library/recent/' \
  | grep -oE '<(D:)?href>[^<]+</(D:)?href>' | sed -n '2p' | sed -E 's|</?[^>]+>||g')
[ -z "$FNAME" ] && { echo 'no items in recent/'; exit 1; }
curl -sI -u amarnath:gpix-test-pw-2026 "http://127.0.0.1:8080${FNAME}"
```

**expect:** `200 OK`, `Content-Length: <bytes>`, `Content-Type: image/jpeg` (or video/etc.). Note `-I` (not `-X HEAD`) — `curl -X HEAD` waits for a body that never comes and hangs.

**8. PUT a small JPEG → 201, then GET it back and SHA-1 compare**

This is the only round-trip that proves bytes survive end-to-end. Save a known file as `./smoke.jpg` first.

```bash
sha1sum ./smoke.jpg
MSYS_NO_PATHCONV=1 curl -i -u amarnath:gpix-test-pw-2026 \
  --upload-file ./smoke.jpg \
  'http://127.0.0.1:8080/dav/library/recent/upload-smoke.jpg'
```

**expect:** `201 Created`. The path's `recent/` segment is **ignored** for placement — the upload always lands in the library root and surfaces wherever Google ranks it. (Note: a second PUT to the same path also returns 201, not 204 — `OpenFile` never probes for an existing file at `fs.go:171-174`, so `x/net/webdav` always sees a new resource. Two PUTs create two library items; gpmc dedupes only on byte SHA-1, not filename.)

**9. Find the uploaded file in the listing**

```bash
UPLOAD_HREF=$(curl -s -u amarnath:gpix-test-pw-2026 -X PROPFIND -H 'Depth: 1' \
  'http://127.0.0.1:8080/dav/library/recent/' \
  | grep -oE '<(D:)?href>[^<]+upload-smoke[^<]*</(D:)?href>' \
  | head -1 | sed -E 's|</?[^>]+>||g')
echo "found at: $UPLOAD_HREF"
```

**expect:** non-empty `$UPLOAD_HREF`. If empty, check `page-2/` — Google may have placed it past page-1. Don't grep `page-2/`+ blindly; use the web UI at `http://127.0.0.1:8080/` (browse view) which reflects uploads before DAV pagination re-fetches.

**10. GET it back and compare bytes**

```bash
curl -s -u amarnath:gpix-test-pw-2026 -o /tmp/dav-fetch.bin "http://127.0.0.1:8080${UPLOAD_HREF}"
sha1sum /tmp/dav-fetch.bin ./smoke.jpg
```

**expect:** identical SHA-1s. This is the only end-to-end correctness proof in the runbook — passing it means PUT, gpmc upload, gpmc list, GET, and (if encryption is on) disguise round-trip all work.

**11. PROPFIND Depth: 0 on the file (not a directory) → 207**

```bash
curl -i -u amarnath:gpix-test-pw-2026 -X PROPFIND -H 'Depth: 0' \
  "http://127.0.0.1:8080${UPLOAD_HREF}"
```

**expect:** `207 Multi-Status` with one `<response>` containing `<getcontentlength>`, NOT 404. Catches regressions where the FS confuses files and dirs.

**12. DELETE the test file → 204**

> *This trashes only the file you just uploaded; the real library is untouched.*

```bash
curl -i -u amarnath:gpix-test-pw-2026 -X DELETE "http://127.0.0.1:8080${UPLOAD_HREF}"
```

**expect:** `204 No Content`. Item moves to Google Photos trash (60-day retention). If you get 404, the file was indexed into a page other than what `$UPLOAD_HREF` points to — re-run step 9.

**13. MKCOL → 403 (collections are virtual)**

```bash
curl -i -u amarnath:gpix-test-pw-2026 -X MKCOL 'http://127.0.0.1:8080/dav/library/newdir/'
```

**expect:** `403 Forbidden`, body literally `collections are read-only\n` (LF byte), `Content-Type: text/plain; charset=utf-8`. This is gpix's `serve()` override (`server.go:51-55`) — x/net/webdav alone would return 405.

**14. LOCK / UNLOCK round-trip**

Mac Finder issues LOCK on every upload, so we verify it works:

```bash
LOCK_RESP=$(curl -s -i -u amarnath:gpix-test-pw-2026 -X LOCK \
  -H 'Content-Type: application/xml' -H 'Timeout: Second-60' \
  -d '<?xml version="1.0"?><d:lockinfo xmlns:d="DAV:"><d:lockscope><d:exclusive/></d:lockscope><d:locktype><d:write/></d:locktype><d:owner>test</d:owner></d:lockinfo>' \
  'http://127.0.0.1:8080/dav/library/recent/locktest.txt')
echo "$LOCK_RESP" | head -20
TOKEN=$(echo "$LOCK_RESP" | grep -i '^Lock-Token:' | sed -E 's/.*<([^>]+)>.*/\1/' | tr -d '\r')
curl -i -u amarnath:gpix-test-pw-2026 -X UNLOCK \
  -H "Lock-Token: <$TOKEN>" 'http://127.0.0.1:8080/dav/library/recent/locktest.txt'
```

**expect:** LOCK returns `200 OK` with a `Lock-Token` header. UNLOCK with that token returns `204 No Content`. The in-memory lock system is `webdav.NewMemLS()` (`server.go:29`).

**15. PROPPATCH / COPY / MOVE — what actually happens**

```bash
curl -sI -u amarnath:gpix-test-pw-2026 -X PROPPATCH 'http://127.0.0.1:8080/dav/library/recent/' \
  -H 'Content-Type: application/xml' \
  -d '<?xml version="1.0"?><d:propertyupdate xmlns:d="DAV:"><d:set><d:prop><d:displayname>x</d:displayname></d:prop></d:set></d:propertyupdate>' \
  -o /dev/null -w '%{http_code}\n'
curl -sI -u amarnath:gpix-test-pw-2026 -X COPY 'http://127.0.0.1:8080/dav/library/recent/' \
  -H 'Destination: http://127.0.0.1:8080/dav/library/copy/' -o /dev/null -w '%{http_code}\n'
curl -sI -u amarnath:gpix-test-pw-2026 -X MOVE 'http://127.0.0.1:8080/dav/library/recent/' \
  -H 'Destination: http://127.0.0.1:8080/dav/library/moved/' -o /dev/null -w '%{http_code}\n'
```

**expect:** PROPPATCH returns 207 (succeeds against x/net/webdav's in-memory property store with no effect on the virtual FS). COPY and MOVE hit `Mkdir`/`Rename` `ErrForbidden` and return 403 (or 409 depending on intermediate steps). Treat OPTIONS' `Allow` list as understated, not as the source of truth.

**16. PROPFIND with bad path → 404**

```bash
curl -i -u amarnath:gpix-test-pw-2026 -X PROPFIND -H 'Depth: 0' 'http://127.0.0.1:8080/dav/library/recent/no-such-file.jpg'
curl -i -u amarnath:gpix-test-pw-2026 -X PROPFIND -H 'Depth: 0' 'http://127.0.0.1:8080/dav/foo/'
```

**expect:** `404 Not Found` for both.

**17. Wrong password → 401 (with ≥ 300 ms delay) — run LAST**

> *Each wrong-cred attempt blocks for ≥ 300 ms on the server (`auth.go:24`); there's no runtime knob to disable it. Batch these at the end and budget ~3 s for 10 attempts.*

```bash
time curl -i -u amarnath:WRONG -X OPTIONS 'http://127.0.0.1:8080/dav/'
```

**expect:** `401`, body `unauthorized\n`, `real >= 0.3s` (may be higher on slow networks — the 300 ms is server-side only). Wrong username produces an identical response and timing.

## Verify disguise + encryption round-trip

**Reset the on-disk cache between tests** so the next GET actually re-probes:

```bash
rm -f /tmp/gpix-dav-* /tmp/gpix-davup-* 2>/dev/null
```

**V2 (encrypted) — with `WEBDAV_ENC_PASSPHRASE` set:**

```bash
sha1sum ./report.pdf
MSYS_NO_PATHCONV=1 curl -i -u amarnath:gpix-test-pw-2026 --upload-file ./report.pdf \
  'http://127.0.0.1:8080/dav/library/recent/report.pdf'     # expect 201

DISGUISED=$(curl -s -u amarnath:gpix-test-pw-2026 -X PROPFIND -H 'Depth: 1' \
  'http://127.0.0.1:8080/dav/library/recent/' \
  | grep -oE '<(D:)?href>[^<]+report\.pdf[^<]*</(D:)?href>' \
  | head -1 | sed -E 's|</?[^>]+>||g')
curl -s -u amarnath:gpix-test-pw-2026 -o /tmp/report-back.pdf "http://127.0.0.1:8080${DISGUISED}"
sha1sum /tmp/report-back.pdf ./report.pdf
ls /tmp/gpix-dav-*   # one temp file populated by extractToTemp
```

**expect:** SHA-1 matches. A second GET to the same URL within 10 min reuses the temp cache (`fileCacheTTL`, `pkg/webdav/cache.go`) — no new probe in server logs, same `/tmp/gpix-dav-*` file.

**V1 (unencrypted disguise) — auto-extracts with NO env required.** Restart gpix with `WEBDAV_ENC_PASSPHRASE` commented out, then GET a known V1 disguised file (one uploaded before encryption was enabled, or via a gpix build that used plain disguise):

```bash
curl -s -u amarnath:gpix-test-pw-2026 -o /tmp/v1-back.bin "http://127.0.0.1:8080${V1_HREF}"
sha1sum /tmp/v1-back.bin   # must match the original
```

This proves the `disguise.Extract` path (`fs.go:389-416`) runs without a passphrase for V1; only V2 (encrypted) payloads need `WEBDAV_ENC_PASSPHRASE`.

## Mount as a drive

### Windows

**Native WebClient (Map Network Drive):** Windows refuses Basic over HTTP by default. Run in **elevated PowerShell** (not git-bash — `Set-ItemProperty` is PowerShell-only):

```powershell
Set-ItemProperty -Path 'HKLM:\SYSTEM\CurrentControlSet\Services\WebClient\Parameters' -Name BasicAuthLevel -Value 2
Set-ItemProperty -Path 'HKLM:\SYSTEM\CurrentControlSet\Services\WebClient\Parameters' -Name FileSizeLimitInBytes -Value 0xffffffff
net stop webclient; net start webclient
```

Then File Explorer → "Map network drive" → `http://127.0.0.1:8080/dav/` → enter `amarnath` / your password → drive Z appears. The `FileSizeLimitInBytes` tweak raises the WebClient cap to its documented maximum of ~4 GiB; the service cannot exceed that.

**[WinFsp](https://winfsp.dev/) + [rclone](https://rclone.org/) (preferred):**

```bash
rclone mount gpix: Z: --vfs-cache-mode writes
```

### macOS

Finder → `Cmd+K` → `http://127.0.0.1:8080/dav/` → credentials. macOS Finder issues `LOCK` then chunked PUTs and may stall on some servers; if uploads hang, mount via rclone instead.

### Linux ([davfs2](https://savannah.nongnu.org/projects/davfs2/))

```bash
sudo apt install davfs2
echo "/mnt/gpix amarnath gpix-test-pw-2026" | sudo tee -a /etc/davfs2/secrets
sudo chmod 600 /etc/davfs2/secrets
sudo mkdir -p /mnt/gpix
sudo mount -t davfs http://127.0.0.1:8080/dav/ /mnt/gpix
ls /mnt/gpix/library/recent/
```

### [rclone](https://rclone.org/) (any OS, recommended)

`rclone config create` takes `key=value` pairs (not space-separated args):

```bash
rclone config create gpix webdav \
  url=http://127.0.0.1:8080/dav/ \
  vendor=other \
  user=amarnath \
  pass="$(rclone obscure 'gpix-test-pw-2026')"

rclone ls gpix:library/recent
rclone copy ./local.jpg gpix:library/recent/        # use recent/, NOT library/test/ — only recent and page-N exist
rclone copyto gpix:library/recent/test.jpg ./fetched.jpg
rclone delete gpix:library/recent/test.jpg
```

Always use `vendor=other` — `nextcloud`/`owncloud` send PROPPATCH/extension calls gpix doesn't claim. Avoid `gpix:library/test/` and similar — `library/test/` is not a valid page label (only `recent` and `page-N` exist per `fs.go:121-129`); rclone's PROPFIND-on-parent will 404.

## Edge cases & known limits

- **Depth: infinity PROPFIND** — x/net/webdav supports it, but `/dav/library/` Readdir is capped at **50 pages** (`maxPages` in `fs.go:260`) and each `page-N` for N≥2 re-walks N-1 cursors **per request** (no cursor cache, `fs.go:131-138`). Deep listings are slow; an infinity PROPFIND on `/dav/` will hammer the upstream.
- **Large uploads** — `ReadHeaderTimeout` is 10 s, but no `WriteTimeout` is set, so multi-GB PUTs have time. Net path may still impose its own limits (Windows WebClient: 4 GB; davfs2: 2 GB by default in older versions).
- **Filenames with spaces / unicode** — URL-escape them (`%20`); curl needs `--globoff` if the name has `{` `}` `[` `]`. `cleanPath` does `strings.TrimSpace` on the whole path, so trailing whitespace in a filename will 404. **No Unicode normalization** — NFC vs NFD mismatches between client and Google's stored `Filename` will 404 on GET/DELETE.
- **Duplicate PUTs** — two PUTs to the same path each return 201 (the FS never probes for an existing file), and each creates a separate library item; gpmc dedupes only on SHA-1 of bytes, not filename.
- **Disguised filenames** — listings show the raw `Filename` including the cover extension; an mp4 disguised as `.jpg` appears as `.jpg`. The bytes you GET back are the original mp4 after transparent extraction.
- **DELETE semantics** — items go to Google Photos trash (60-day retention), not hard-deleted. Restore from `photos.google.com/trash`.
- **WebDAV file cache TTL** — extracted disguised payloads live in OS temp for **10 min** (`pkg/webdav/cache.go fileCacheTTL`); after that, next GET re-probes and re-extracts. Don't confuse with `pkg/web/urlcache.go` (web UI URL cache, expire-based, no reaper).
- **PROPPATCH / COPY / MOVE** — not in OPTIONS' `Allow`, but x/net/webdav handles them; PROPPATCH returns 207 against the in-memory property store with no FS effect, COPY/MOVE return 403 from the read-only FS. The Allow header understates capability.
- **MKCOL during Finder/Explorer uploads** — clients sometimes implicitly MKCOL a parent before PUT; gpix returns 403, which some clients abort on. rclone tolerates this; Finder may not.
- **Per-page de-duplication** — two items with identical `Filename` in the same page collapse to one entry (`fs.go:223-230`); only the first is reachable.

## Cleanup

Discover-then-delete (avoid hardcoded placeholders):

```bash
for NAME in upload-smoke report.pdf locktest.txt; do
  HREF=$(curl -s -u amarnath:gpix-test-pw-2026 -X PROPFIND -H 'Depth: 1' \
    'http://127.0.0.1:8080/dav/library/recent/' \
    | grep -oE "<(D:)?href>[^<]+${NAME}[^<]*</(D:)?href>" \
    | head -1 | sed -E 's|</?[^>]+>||g')
  [ -n "$HREF" ] && curl -s -u amarnath:gpix-test-pw-2026 -X DELETE "http://127.0.0.1:8080${HREF}" -w "deleted %{http_code} ${HREF}\n"
done
rm -f /tmp/gpix-dav-* /tmp/gpix-davup-* /tmp/dav-fetch.bin /tmp/report-back.pdf
```

Or visit `photos.google.com/trash` to restore within 60 days. Then stop gpix (`Ctrl+C`) to free port 8080.

## Failure interpretation

| Symptom | Likely cause | Fix |
|---|---|---|
| `404` on every `/dav/*` after restart | WEBDAV_* vars missing or load error; `webdav disabled` at DEBUG | Add `WEBDAV_USERNAME` + `WEBDAV_PASSWORD_HASH`, restart, look for `webdav mounted` |
| `401` with creds you swear are right | `.env` parser ate a `$` in the bcrypt hash, or trailing `\r` from Windows line endings | Single-quote the hash; `dos2unix .env` (or `sed -i 's/\r$//' .env`) |
| `404` on `/dav/library/recent/` | Empty library, or upstream gpmc error swallowed | Check gpix logs; confirm `GP_AUTH_DATA` still valid via web UI |
| Stuck on Windows "Map network drive" | `BasicAuthLevel` not set, or WebClient cached old failure | Set reg key, `net stop webclient && net start webclient`, retry |
| PUT returns 201 but item never visible in `recent/` | Google placed it past page-1, or DAV pagination hasn't re-fetched yet | Wait 30-60 s for Google to ingest; check `/browse` web UI (reflects uploads faster than DAV); then PROPFIND `page-2/` |
| rclone: "unsupported feature" | `vendor=nextcloud`/`owncloud` | Reconfigure with `vendor=other` |
| GET on disguised file returns garbage / errors | V2 (encrypted) payload, `WEBDAV_ENC_PASSPHRASE` unset or wrong | Set the passphrase to the one used at upload time, restart |
| Auth attempts take ~300 ms each | Intentional — bcrypt cost + sleep on credential mismatch (`auth.go:24`) | Don't loop brute-force; budget for it in tests |
| Wrong-username and wrong-password look identical | Timing-equalized on purpose (`subtle.ConstantTimeCompare` + bcrypt) | Not a bug; this is the contract |
| `curl -X HEAD` hangs forever | curl waits for a body the server never sends | Use `curl -I` or `curl --head` instead |
| URL path mangled to `C:/Program Files/Git/dav/...` | MSYS path conversion on git-bash | Single-quote URLs, or prefix command with `MSYS_NO_PATHCONV=1` |

## Closer

Passing every check above proves the WebDAV implementation does what its contract promises: the mount is gated correctly by both env vars, Basic auth is timing-equalized against username and password probing, the three-level virtual tree (`/dav/` → `library/` → page labels → files) lists exactly the expected entries with no spurious siblings, MKCOL is intercepted with gpix's custom 403 before x/net/webdav can return its default 405, PUT-then-GET round-trips bytes with a matching SHA-1 (including transparent V1 and V2 disguise extraction when the passphrase is set), DELETE moves items to Google's trash rather than hard-deleting, LOCK/UNLOCK succeeds via the in-memory lock system Finder depends on, and PROPPATCH/COPY/MOVE behave consistently with the read-only virtual FS even though they're absent from the advertised `Allow` header. If all 17 steps pass plus the disguise round-trip, the mount is production-ready for the loopback use case it was designed for.