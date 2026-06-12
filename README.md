# gpix

Your Google Photos library, on your terms.

gpix is a self-hosted Google Photos client written in Go. One binary, four ways to use it:

- **Web UI** — browse, view, stream videos with seek, upload from your browser, delete items. Minimalist black-and-white design, dark/light theme, fully responsive. All assets embedded.
- **CLI** — upload files or whole folders from the terminal.
- **Telegram bot** — `/upload`, `/get`, `/list`, `/info` against your library from any chat.
- **Universal file storage** — PDFs, archives, executables, any file gets transparently wrapped as a 1-second MP4 with the original bytes preserved. Uploaded as a "video", recovered byte-identical on download. Effectively unlimited cloud storage for arbitrary files.

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

Web UI default: `http://127.0.0.1:8080`.

---

## Auth: getting `GP_AUTH_DATA`

gpix talks the same mobile protocol the Android Google Photos app uses. To prove to Google that you're a legitimate client, you need a long-lived master token plus device fingerprint, packaged as a query-string blob called `auth_data`.

There's no built-in login flow. You extract this once from a real Android app session. Two paths:

**ReVanced + adb (no root):**

1. Install [Google Photos ReVanced](https://github.com/inotia00/ReVanced_Extended) and GmsCore on any Android device or emulator.
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

```bash
# Generate a password hash
go run . -hashpw
# (paste password, copy the printed $2a$12$... hash)

# Create config
cp gpix-web.conf.example gpix-web.conf
# Edit: set username, paste hash into password_hash
```

Config:

```toml
listen = 127.0.0.1:8080
username = you
password_hash = $2a$12$replace_me
device_profile = pixel-xl
max_concurrent_uploads = 2
session_days = 30
stream_token_ttl_minutes = 60
```

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
3. DM [@userinfobot](https://t.me/userinfobot) to learn your numeric user id.

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

The UI clearly marks disguised items — they show as file cards with the original extension, not video players.

**Caveat:** This is obfuscation, not encryption. Anyone with the media key and this format spec can recover the bytes. Don't store secrets in there without a separate encryption layer.

---

## Build for Linux

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o gpix .
```

Fully static, ~25 MB stripped. Drop the binary on any Linux box with `.env` and `gpix-web.conf` next to it.

---

## Project layout

```
gpix/
├── main.go               ← single entrypoint, four sub-modes via -mode flag
├── pkg/
│   ├── gpmc/             ← Google Photos protocol client
│   │   ├── auth.go             OAuth bearer cache + refresh
│   │   ├── upload.go           four-step upload pipeline
│   │   ├── download.go         file fetch via PhotosPrepareDownload
│   │   ├── list.go             library listing + pagination
│   │   ├── delete.go           trash + permanent delete
│   │   ├── stream.go           HLS manifest fetch
│   │   ├── libstate_blob.go    embedded prebuilt protobuf request
│   │   └── gpmcpb/             generated protobuf code
│   ├── bridge/           ← Telegram bot (uses pkg/gpmc + pkg/disguise)
│   ├── web/              ← Web UI + HTTP server
│   │   ├── templates/          embedded html/template files
│   │   └── static/             embedded styles.css, hls.min.js, Plyr, favicon
│   └── disguise/         ← MP4 wrapper for non-media files
│       ├── wrapper.mp4         3 KB precompiled 1-second blue MP4
│       └── format.go           Wrap / Extract / Sniff
└── proto/
    └── gpmc/v1/messages.proto
```

Notable design choices:

- **Single static binary.** Templates, CSS, JS, hls.js, Plyr, MP4 wrapper, protobuf schemas — all `//go:embed`-ed. No external assets at runtime.
- **Direct-to-Google.** There's no gpix server in the middle. Your binary talks straight to Google's mobile API endpoints.
- **Streamable video** uses the same HLS infrastructure YouTube uses (`googlevideo.com` CDN), which means proper HTTP Range support and adaptive bitrate.
- **Byte-identical round-trips** for both uploads and downloads (`/raw` endpoint). Verified empirically against the live API.

---

## Trust model

- **Your photos stay in your Google account.** If you stop using gpix tomorrow, everything is still there in the regular Google Photos app/web.
- **No third party.** The binary talks directly to Google. No relay servers, no analytics, no telemetry.
- **Auth tokens stay local.** `GP_AUTH_DATA` lives in `.env` on your machine and never leaves it except to authenticate to Google.
- **Web UI is single-user.** Bind to `127.0.0.1` (default) or put it behind a reverse proxy with TLS if you want remote access.

---

## License

[MIT](LICENSE).

Bundled third-party assets:

- [hls.js](https://github.com/video-dev/hls.js) — Apache-2.0
- [Plyr](https://github.com/sampotts/plyr) — MIT
- [Inter](https://rsms.me/inter/) — OFL (loaded from rsms.me, not bundled)
- `wrapper.mp4` is a 1-second solid-color video generated by ffmpeg, no copyrighted content.
