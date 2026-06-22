# gpix

Your Google Photos library, on your terms.

gpix is a self-hosted Google Photos client written in Go. One binary, several ways to use it:

- **Web UI** — browse, view, stream videos with seek, upload from your browser, delete items. Minimalist black-and-white design, dark/light theme, fully responsive. All assets embedded.
- **S3-compatible gateway** — point `aws`, `mc`, `boto3`, `rclone`, or any S3 client at your library. AWS Signature V4 auth, keys generated and rotated from the web UI.
- **WebDAV gateway** — mount your library in Finder, Windows Explorer, or `rclone`. Basic auth with your login password or a revocable app password.
- **Client-side encryption** — optionally encrypt every upload with a gpix-managed key before it leaves your machine. Google stores only opaque video; no one without your key can see your media. Inside gpix, encrypted photos still display as photos (decrypted on the fly, with locally-generated thumbnails).
- **Password-protected sharing** — create expiring, optionally password-protected public links to a single item or a whole **gallery** of selected items, stored in SQLite. Encrypted items are decrypted server-side for the recipient.
- **Single sign-on (Logto/OIDC)** — optional Logto login with an email allowlist and a max-users registration cap, alongside the built-in password login.
- **CLI** — upload files or whole folders from the terminal.
- **Telegram bot** — `/upload`, `/get`, `/list`, `/info` against your library from any chat (optional).
- **Universal file storage** — PDFs, archives, executables, any file gets transparently wrapped as a 1-second MP4 with the original bytes preserved. Uploaded as a "video", recovered byte-identical on download. Text/markdown documents additionally render to a readable **image** in the UI while staying downloadable as the original. Effectively unlimited cloud storage for arbitrary files.

gpix talks the mobile Google Photos protocol directly, which means **uploads count as original quality without consuming your storage quota** (Pixel device profile). Everything else — dedup, video streaming, thumbnails — is wired into the same fast path.

---

## Why

The official Google Photos app and web UI are great for casual use. They're less great when you want to:

- Bulk-upload photos from a server or VPS without a phone
- Stream a 4 GB video to VLC with proper seeking
- Use Google Photos as backing storage for anything that isn't a photo or video
- Drive uploads from a script or a Telegram chat
- Self-host a clean, fast UI for your own library without trusting a third party

gpix does those.

---

## Quick start

```bash
# One-time: fetch the two pure-Go dependencies added for sharing, the
# listing cache, and document-to-image rendering (all CGO-free).
go get modernc.org/sqlite golang.org/x/image
go mod tidy

# Build
go build .

# Or run without building
go run .
```

On first run gpix wants:

1. A Google Photos `auth_data` string in `.env` (see [Auth](#auth-getting-gp_auth_data) below).
2. A web config + bcrypt password hash if you want the web UI (see [Web UI](#web-ui)).
3. Telegram bot credentials in `.env` if you want the bot (see [Telegram bot](#telegram-bot)).

Then:

```bash
go run .                        # bot + web together
go run . -mode web              # web UI only
go run . -mode bot              # bot only
go run . -cli ./photos          # CLI uploader
go run . -hashpw                # generate a bcrypt password hash
```

Web UI default: binds `0.0.0.0:8080`, reachable at `http://<your-host>:8080` (or `http://localhost:8080` on the same machine).

---

## Auth: getting `GP_AUTH_DATA`

gpix talks the same mobile protocol the Android Google Photos app uses. To prove to Google that you're a legitimate client, you need a long-lived master token plus device fingerprint, packaged as a query-string blob called `auth_data`.

There's no built-in login flow. You extract this once from a real Android app session. Two paths:

**ReVanced + adb (no root):**

1. Install Google Photos ReVanced (grab the patched APK from [revanced-magisk-module releases](https://github.com/j-hc/revanced-magisk-module/releases/latest)) and GmsCore on any Android device or emulator.
2. Plug in via USB, enable USB debugging.
3. Run `adb logcat | grep "auth%2Fphotos.native"`.
4. Sign into Google Photos in the app.
5. Copy the line that starts with `androidId=` — that's your `auth_data`.

**Rooted device + HTTP Toolkit:**

1. Configure HTTP Toolkit to intercept traffic from the Google Photos app.
2. Look for the POST to `https://android.googleapis.com/auth` with body containing `service=oauth2:...photos.native`.
3. Copy the entire form body.

Either way you end up with one long string like:

```
androidId=3fe3659f2757ca27&app=com.google.android.apps.photos&...&Token=aas_et/AKpp...
```

Paste it into `.env`:

```env
GP_AUTH_DATA=androidId=...&Token=aas_et/...
```

The token is account-bound and effectively permanent. gpix exchanges it for short-lived OAuth bearers on demand, caching them in memory.

---

## Web UI

The web UI is the main way most people will use gpix.

### Setup

Generate a password hash, then configure gpix with **`.env` (recommended)** or the optional `gpix-web.conf` file — every setting can come from either, and environment variables win when both are set.

```bash
# Generate a password hash
go run . -hashpw
# (paste password, copy the printed $2a$12$... hash)
```

**Option A — `.env` (everything in one place):**

```bash
cp .env.example .env
# Set GP_AUTH_DATA, GPIX_USERNAME, GPIX_PASSWORD_HASH (and any gateways you want)
```

```env
GP_AUTH_DATA=androidId=...&Token=aas_et/...
GPIX_USERNAME=you
GPIX_PASSWORD_HASH=$2a$12$replace_me
GPIX_LISTEN=0.0.0.0:8080
# optional: GPIX_S3_LISTEN, GPIX_WEBDAV_LISTEN, GPIX_IMMICH_LISTEN, SERVER_URL, ...
```

**Option B — `gpix-web.conf`:**

```bash
cp gpix-web.conf.example gpix-web.conf   # then edit username + password_hash
```

```toml
listen = 0.0.0.0:8080
username = you
password_hash = $2a$12$replace_me
device_profile = pixel-xl
max_concurrent_uploads = 2
session_days = 30
stream_token_ttl_minutes = 60
```

Runtime state files (`secret.key`, `gateways.json`, `encryption.key`, `shares.db`) are always created on disk — set `GPIX_DATA_DIR` to choose where (defaults to the working directory). The **Telegram bot is optional**: leave `TG_BOT_TOKEN` empty and `gpix` (or `-mode all`) just runs the web + gateways.

On first run gpix generates a 32-byte `secret.key` next to the config (used to sign session cookies and media-share tokens). Keep it safe; rotating it logs everyone out.

### Features

- **Library browse** with paginated grid, lazy-loaded thumbnails
- **Photo view** — full-resolution display, click to download original
- **Video view** — Plyr player with HLS adaptive streaming, manual quality picker (192p → 1920p), proper seeking
- **Stream URLs** — copy a signed URL straight into VLC for any quality level
- **Upload** — drag-and-drop multi-file, live progress via Server-Sent Events
- **Delete** — move items to Google Photos trash from the UI
- **Disguised files** show with file icon + extension badge instead of a video player

Single-user. Sign in once, session lasts 30 days by default.

---

## Gateways: S3 & WebDAV

gpix can expose the same library — including disguised non-media files — over two standard protocols, each on its own port, running alongside the web UI. Uploads still go through the original-quality Pixel path, and downloads transparently un-disguise wrapped files. The mapping is flat: one bucket / one root collection of objects keyed by filename.

### Turn it on

Both gateways are **off by default**. Enable the ones you want by adding a `*_listen` line to `gpix-web.conf` and restarting gpix:

```toml
# gpix-web.conf
s3_listen     = 0.0.0.0:9000     # S3 API on :9000 (all interfaces)
s3_bucket     = gpix             # cosmetic; default "gpix"
s3_region     = us-east-1        # cosmetic; any signed region is accepted

webdav_listen = 0.0.0.0:8081     # WebDAV on :8081 (all interfaces)
```

Use `127.0.0.1` instead of `0.0.0.0` if you want an endpoint reachable only from the same machine.

That's it — no credentials needed in the config file. Run gpix as usual:

```bash
go run . -mode web      # or -mode all
```

> **Network exposure.** Binding to `0.0.0.0` (the default here) listens on every interface, so the ports are reachable from your whole LAN — anyone who can route to the host can hit them. SigV4 (S3) and Basic auth (WebDAV) gate access, but the traffic itself is **plain HTTP with no transport encryption**. For anything beyond a trusted local network, put gpix behind a reverse proxy with TLS, restrict with a firewall, or use an SSH tunnel and bind to `127.0.0.1` instead.

### Generate & save credentials (web UI)

Open the web UI → **Connections** (top nav). For each gateway you'll see its endpoint URL and controls to mint credentials:

- **S3** — click **Generate keys**. gpix creates an **Access Key ID** (public, like `GPIX…`) and a **Secret Access Key**. Use **Show** to reveal the secret and **Copy** to grab it. The secret is shown masked by default; **save it in your client now**. **Regenerate** rotates the pair (old keys stop working instantly); **Clear** disables S3 auth entirely.
- **WebDAV** — your normal login username/password always works. Optionally click **Generate app password** to mint a separate, revocable password you can paste into a client without exposing your main one.

Credentials are stored in `gateways.json` next to `secret.key` (file mode `0600`, git-ignored). Rotating a key in the UI takes effect immediately — no restart.

### Use it — S3

```bash
export AWS_ACCESS_KEY_ID=GPIX...           # from the Connections page
export AWS_SECRET_ACCESS_KEY=...           # the secret you copied

aws --endpoint-url http://127.0.0.1:9000 s3 ls s3://gpix/
aws --endpoint-url http://127.0.0.1:9000 s3 cp ./report.pdf s3://gpix/
aws --endpoint-url http://127.0.0.1:9000 s3 cp s3://gpix/report.pdf ./out.pdf
aws --endpoint-url http://127.0.0.1:9000 s3 rm s3://gpix/report.pdf
```

Works the same with `mc` (MinIO client), `s3cmd`, `boto3`, or `rclone`'s S3 backend. Supported operations: list buckets, list objects (v1 & v2, with `prefix`/`delimiter`), HEAD/GET (incl. `Range`), PUT, DELETE, and batch delete. Multipart upload, ACLs, versioning, and tagging are **not** implemented, so configure clients for single-part puts (`boto3`: a large `multipart_threshold`).

### Use it — WebDAV

```bash
# rclone
rclone config create gpix webdav url http://127.0.0.1:8081 vendor other \
  user your-username pass <app-password-or-login-password>
rclone ls gpix:
rclone copy ./report.pdf gpix:

# curl
curl -u your-username:<password> -T report.pdf http://127.0.0.1:8081/report.pdf
curl -u your-username:<password> http://127.0.0.1:8081/report.pdf -o out.pdf
```

**Finder (macOS):** *Go → Connect to Server* → `http://127.0.0.1:8081`.
**Windows:** *Map network drive* → same URL.

> **Heads-up on duplicates.** Google Photos allows multiple items with the same filename; the gateways expose only the newest one per name. Treat object keys as filenames, and prefer unique names when uploading.

### Testing the gateways

The protocol layers run against any `store.Backend`. A standalone harness wires them to an **in-memory** backend so you can test with real clients without touching Google Photos:

```bash
# S3 + WebDAV on an in-memory store, no Google auth required
go run ./cmd/gpix-gateway-test \
  -s3 127.0.0.1:9000 -dav 127.0.0.1:8081 \
  -access test -secret testsecret -bucket gpix -user gpix -pass gpix

# In another shell:
go test ./pkg/s3/...                       # SigV4 unit tests (AWS test vectors)
pip install boto3 && python3 test/s3_smoke.py
./test/webdav_smoke.sh
```

---

## CLI

```bash
go run . -cli photo.jpg
go run . -cli -quality saver photo.jpg
go run . -cli -recursive ./vacation
```

| Flag | Meaning |
|---|---|
| `-auth <str>` | auth_data, defaults to `$GP_AUTH_DATA` |
| `-quality original\|saver\|quota` | upload quality / quota behavior |
| `-profile pixel-xl\|pixel-5` | device fingerprint for the session |
| `-concurrency <n>` | parallel uploads (default 1) |
| `-recursive` | descend into directories |
| `-force` | skip the dedup check |
| `-delete-after` | delete local file after successful upload |

Output: `OK <path>\t<media_key>` or `SKIP <path>\t<media_key>` per file on stdout; events on stderr.

The CLI is the fastest way to bulk-upload from a server. No web UI, no bot, just files in → media keys out.

---

## Telegram bot

If you want to push files to Google Photos from a Telegram chat (or pull files out into a chat), gpix can run a bot.

### Setup

1. Open [@BotFather](https://t.me/BotFather), run `/newbot`, save the token.
2. Visit [my.telegram.org/apps](https://my.telegram.org/apps), create an app, save `api_id` and `api_hash`.

Add to `.env`:

```env
TG_BOT_TOKEN=123456789:ABC...
TG_API_ID=12345678
TG_API_HASH=abcd...
TG_OWNER_ID=987654321
```

The bot only honors commands from `TG_OWNER_ID` — every other message is silently ignored.

### Commands

| Command | Behavior |
|---|---|
| `/upload` | Reply to any file/photo/video with `/upload` → bot uploads it to Google Photos, replies with the media key. Non-media files are auto-disguised. |
| `/get <media_key>` | Bot fetches the file from Google Photos and sends it back to the chat. Auto-unwraps disguised files. |
| `/list [n]` | Shows the N most recent items in the library. |
| `/info` | Account email, device profile, concurrency. |

File size cap: 2 GB for bot accounts; 4 GB for user accounts with Telegram Premium.

---

## Disguised files

Google Photos only accepts photos and videos. gpix gets around this with a simple trick: wrap arbitrary files in a tiny valid MP4 container, then append the original bytes after the container's declared end. Google's pipeline stops reading at the MP4 trailer and stores the rest of the bytes verbatim at original quality.

When you upload `report.pdf` through the web UI or `/upload` in the bot:

1. gpix detects it's not media (MIME + extension + magic-byte sniff).
2. Builds a payload: `[3 KB precompiled wrapper.mp4][16-byte magic][filename length][filename][payload length][payload]`.
3. Uploads as `report.pdf.mp4`. Google Photos sees a 1-second solid-color video. The trailing PDF bytes are preserved.

When you download it:

1. gpix scans the first 8 KB of the file from Google's servers.
2. Finds the magic marker, parses the header, strips the wrapper.
3. Returns the original `report.pdf` with the right Content-Type and filename.

The UI marks disguised items by their original extension rather than as video players. **Text and markdown documents** (and source/config/data text files) go a step further: gpix renders their content to a readable monospace **image**, so they appear as a viewable page in the grid and on the view screen (the filename keeps its real extension so you can tell it's a document). The image is generated locally from the decrypted original and cached; **Download** always returns the original file byte-for-byte. (PDFs and other binary documents still show as file cards — rendering those needs a PDF rasteriser that isn't pure-Go.)

**Caveat:** This is obfuscation, not encryption. Anyone with the media key and this format spec can recover the bytes. For real privacy, turn on **encryption** (below), which layers AES-256 on top of the disguise.

---

## Encryption

Disguising hides *what kind* of file something is, but the bytes are still there for anyone who can read them. Encryption fixes that: with it on, gpix encrypts every upload with a key that never leaves your machine, then disguises the **ciphertext** as a video. Google — and anyone else without your key — only ever stores and sees an opaque 1-second clip. **Only you can see your media.**

### How it works

- **Cipher:** AES-256-GCM in a chunked stream (the age/Tink "STREAM" construction). A fresh 256-bit content key is derived per file via HKDF-SHA256 from your master key and a random salt; the header is authenticated and the final chunk is tagged, so tampering, reordering, and truncation are all detected.
- **Key:** a single 32-byte master key gpix generates and stores in `encryption.key`, next to `secret.key` (mode `0600`). It is never uploaded.
- **Flow:** `original → encrypt → disguise as .mp4 → Google Photos`. On download (web UI, S3, or WebDAV) gpix detects the encrypted blob and decrypts it transparently, returning the original bytes and filename.

### Turn it on

Web UI → **Connections** → **Media encryption** → *Turn encryption on*. (Or seed it with `encrypt_uploads = true` in `gpix-web.conf` for the first run.) From then on, every new upload is encrypted. The panel shows a short **key fingerprint** so you can confirm which key is active, and a **Download key backup** button.

> **Back up `encryption.key`.** If you lose it, everything encrypted with it is **permanently unreadable** — gpix can't recover it and neither can Google. Use the backup button (or copy the file) and store it somewhere safe. Restoring it later (drop it back next to `secret.key`) restores access; the fingerprint lets you verify it's the right one.

### Trade-offs

- **Inside gpix, encrypted items look normal** — gpix holds the key, so encrypted photos render as photos (with thumbnails it generates and caches locally from the decrypted original) and encrypted videos play in-page. Only *Google* ever sees the opaque blank video; encryption matters at upload, not for viewing in gpix. Locally-generated thumbnails are cached **decrypted** under `gpix-thumbcache/` (next to your data dir) — delete it to purge. Thumbnail generation covers JPEG/PNG/GIF in pure Go; HEIC/WebP/RAW fall back to a blank Google thumbnail. Encrypted videos play via progressive download (no adaptive HLS/seek), and one heuristic edge remains: an encrypted video whose original name ends in `.mp4` is treated as a normal video (and will look blank) — non-`.mp4` videos and all photos are fine.
- Encryption applies to **new** uploads only; existing library items are untouched. Toggling encryption off later still lets you open previously-encrypted items (the key is unchanged).
- The **Telegram bot is not encryption-aware** yet: it won't encrypt its uploads and `/get` returns the still-encrypted blob. Use the web UI, S3, or WebDAV for encrypted media.

---

## Sharing

Create public links to individual items — optionally password-protected and expiring by time or download count. Links are stored in a small SQLite database (`shares.db`, next to `secret.key`). Because decryption happens server-side, you can share an **encrypted** item and the recipient sees the photo without ever touching your key.

**Create one:** open any photo → **Share…** → set an optional password, expiry (hours), max downloads, and whether full-resolution download is allowed → **Create share link**.

**Create a multi-item share:** in the library grid click **Select**, tick any number of photos/videos/documents, then **Share** → set the same options (plus an optional title). The recipient gets one link to a **gallery** of all the items. Manage and revoke links under **Shares** in the top nav.

**Recipient:** opens `https://<your-server>/s/<token>`, enters the password if set, and views/downloads the item(s) — a single photo, or a thumbnail gallery for multi-item shares. Links honor their expiry and download cap automatically.

Share URLs are built from `server_url` / `SERVER_URL` when set, otherwise from the request host — so set `SERVER_URL` if gpix sits behind a reverse proxy.

> Requires the pure-Go SQLite driver. Run once: `go get modernc.org/sqlite` (then `go mod tidy`). It's CGO-free, so the static Docker build keeps working.

---

## Login & access (Logto SSO)

Alongside the built-in username/password login, gpix can sign you in via **[Logto](https://logto.io)** (or any standards-compliant OIDC provider) using the authorization-code + PKCE flow. The password login keeps working, so this is additive.

**Set up:** in Logto, create a **Traditional Web** application, and set its **Redirect URI** to `<server_url>/auth/logto/callback`. Then configure gpix:

```env
GPIX_LOGTO_ENDPOINT=https://your-tenant.logto.app
GPIX_LOGTO_CLIENT_ID=...
GPIX_LOGTO_CLIENT_SECRET=...
SERVER_URL=https://photos.example.com           # used to build the callback URL
GPIX_SIGNUP_ALLOWLIST=you@example.com,@example.com   # optional
GPIX_MAX_USERS=5                                 # optional
```

A **"Sign in with Logto"** button then appears on the login page.

- **Allowlist** — `signup_allowlist` is a comma-separated list of exact emails (`a@b.com`) and/or domains (`@b.com`). On a user's *first* sign-in, their email must match (empty list = anyone Logto authenticates). Already-registered users always pass.
- **Max users** — `max_users` caps how many people can register through OIDC; once reached, new sign-ins are rejected with "registration is closed". `0` = unlimited.
- Registered identities are stored in `users.db` (next to `secret.key`). Sessions are the same signed cookies as password login, so nothing else in the app changes.

> gpix is single-tenant: everyone who signs in shares the same Google Photos library. Logto here controls **who may use this gpix**, not separate per-user libraries.

---

## Immich-compatible API (archived)

An experimental Immich-compatible REST API once lived here. It has been **archived** — the code is kept under [`_archive/immich/`](_archive/immich) (a directory the Go toolchain ignores, so it is not compiled or wired into the binary) but is no longer built or served. To revive it, move the package back under `pkg/`, restore the `immich_listen` config plumbing, and re-add the startup block in `main.go`.

---

## Performance & caching

Listing a large Google Photos library is the slow part of S3 `ListObjects` and WebDAV `PROPFIND`. gpix keeps **one shared, background-refreshed listing cache** (`pkg/library`) that all surfaces read from, so those calls return from memory instead of re-walking Google on every request. The cache refreshes itself periodically and is invalidated on upload/delete.

It's also backed by a **SQLite snapshot** (`cache.db`, via `pkg/cachedb`): on startup gpix serves filenames immediately from the last saved snapshot and refreshes the real listing in the background (stale-while-revalidate), so S3/WebDAV are fast even right after a restart. Thumbnails are served on demand (Google's thumbnail for normal items, locally generated for encrypted photos), so clients load previews first and fetch full originals only when opened.

`SERVER_URL` (env) or `server_url` (config) sets the externally-reachable base URL used for share links and redirects; leave it unset to derive from the request host.

---

## Build for Linux

```bash
go get modernc.org/sqlite golang.org/x/image && go mod tidy   # one-time
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o gpix .
```

Fully static (the SQLite and image deps are pure-Go), ~25 MB stripped. Drop the binary on any Linux box with `.env` (or `gpix-web.conf`) next to it; gpix creates its state files (`secret.key`, `gateways.json`, `encryption.key`, `shares.db`, `cache.db`, `users.db`, `gpix-thumbcache/`) on first run.

---

## Docker

A multi-stage `Dockerfile` builds a static binary into a minimal Alpine image (web assets are embedded, so only CA certs are added). All runtime config and state — `gpix-web.conf`/`.env`, plus the generated `secret.key`, `gateways.json`, `encryption.key`, `shares.db`, `cache.db`, `users.db`, and `gpix-thumbcache/` — live in `/data`, which you mount.

```bash
# Put your config and auth in ./data first:
mkdir -p data
cp gpix-web.conf.example data/gpix-web.conf   # then edit it
printf 'GP_AUTH_DATA=androidId=...&Token=aas_et/...\n' > data/.env

# Build and run
docker build -t gpix .
docker run --rm -p 8080:8080 -p 9000:9000 -p 8081:8081 \
  -v "$PWD/data:/data" gpix
```

Or with Compose (`docker compose up -d` — see `docker-compose.yml`). The image listens on `8080` (web), `9000` (S3), and `8081` (WebDAV); with `0.0.0.0` listen addresses in the config, publishing the ports exposes them on your host. It runs as a non-root user, so make sure the mounted `./data` directory is writable by UID `10001` (or add `--user "$(id -u):$(id -g)"`). Default `CMD` runs web only; use `-mode all` to add the Telegram bot.

---

## Trust model

- **Your photos stay in your Google account.** If you stop using gpix tomorrow, everything is still there in the regular Google Photos app/web.
- **No third party.** The binary talks directly to Google. No relay servers, no analytics, no telemetry.
- **Auth tokens stay local.** `GP_AUTH_DATA` lives in `.env` on your machine and never leaves it except to authenticate to Google.
- **Single shared library.** gpix is single-tenant: the password login (and any Logto users you allow) all share the same Google Photos backend. Logto's allowlist + max-users control *who may sign in*, not separate per-user libraries.
- **Binds `0.0.0.0` by default** (all interfaces) for convenience. Switch to `127.0.0.1` to keep it local, or put it behind a reverse proxy with TLS for remote access. The S3 and WebDAV gateways follow the same rule — see the network-exposure note above.

---

## License

[MIT](LICENSE).
