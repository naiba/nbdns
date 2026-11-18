# v2.1.3 — Self-healing after interrupted writes

Leftovers from a power cut or kill no longer accumulate or permanently break later startups. No configuration migration is required. Existing installations with automatic updates may install this release and restart.

## Changes

- Filter subscription downloads are saved through one fixed, truncated temporary file per list instead of random `.download-*` names. Each interrupted download used to leave a file behind, because the deferred cleanup never ran after a power cut. Leftovers of both naming schemes are removed at startup, and anything occupying the temporary path is cleared instead of failing every later save.
- Statistics and subscription caches share the same atomic replace helper (fixed temporary file, flush, rename).
- If the Badger cache files are unreadable, for example after a power cut, they are discarded once and the cache starts empty. The cache used to fail to open on every later start and stay disabled until the files were deleted by hand. Only Badger's own files are removed; `stats.json` in the same directory is kept, and a database held by another process is never touched.
- Log a disabled or rebuilt cache at the normal level; it used to be visible only with `debug` enabled.

Self-update already writes fixed `.nbdns.new` / `.nbdns.old` files and overwrites them, so interrupted updates do not accumulate files either.

## Validation

Regression tests cover a longer leftover temporary file, repeated saves, an occupied temporary path, removal of interrupted downloads with both naming schemes while the cached list still loads, recovery from corrupted Badger files with `stats.json` preserved, and refusal to wipe a database another process holds. Local release checks run `go test -race ./...`, `go vet ./...`, `go build ./...`, 32-bit CI targets and release cross-compilation.
