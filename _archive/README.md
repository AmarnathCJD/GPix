# Archived code

Directories beginning with `_` are ignored by the Go toolchain, so nothing in
here is compiled by `go build ./...`, tested, or shipped in the binary. It is
kept for reference only.

## immich/

An experimental Immich-compatible REST API (server info, login, timeline,
asset metadata/thumbnails/original download, mobile backup upload, SHA-1
dedup) backed by the gpix Google Photos library.

To revive it:

1. Move the package back into the build tree: `mv _archive/immich pkg/immich`.
2. Restore the `ImmichListen` field and the `immich_listen` cases in
   `pkg/web/config.go` (`applyKey` + `applyEnv`).
3. Re-add the Immich startup block in `main.go` (construct `immich.New(...)`
   and append `imsrv.Run` to `runners`), plus the `gpix/pkg/immich` import.

It depends only on packages that still exist (`gpmc`, `library`, `mediacrypt`,
`disguise`), so it should build again once re-wired.
