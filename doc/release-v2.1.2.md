# v2.1.2 — Safer, smaller statistics saves

Statistics are saved more efficiently and survive power loss. No configuration migration is required. Existing installations with automatic updates may install this release and restart.

## Changes

- Save `data/cache/stats.json` atomically: write a temporary file in the same directory, flush it, then rename it over the old file. The file used to be truncated and rewritten in place, so a power cut during a save — common on routers — left a half-written file and every statistic was lost on the next start. Temporary files left by an interrupted save are removed at startup.
- Write compact JSON instead of indented JSON. Indentation made up about a third of the file, which matters because the file is rewritten every `stats_save_interval` minutes on SD cards and other flash storage. Existing indented files still load.
- Serialize statistics under the lock, but write and flush the file without holding it, so a slow storage card does not delay statistics recording.

## Validation

Regression tests cover compact output, no leftover files, the previous file staying intact when a write fails half-way, file permissions, round-tripping, and cleanup of interrupted temporary files without touching the Badger files that share the directory. Local release checks run `go test -race ./...`, `go vet ./...`, `go build ./...`, 32-bit CI targets and release cross-compilation.

To reduce flash writes further, raise `stats_save_interval` (minutes, default 5); a crash or power cut loses at most that interval of statistics.
