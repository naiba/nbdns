# v2.1.2 — Safer, smaller statistics saves

Statistics are saved more efficiently and survive power loss. No configuration migration is required. Existing installations with automatic updates may install this release and restart.

## Changes

- Save `data/cache/stats.json` atomically: write `.stats.json.tmp` in the same directory, flush it, then rename it over the old file. The file used to be truncated and rewritten in place, so a power cut during a save — common on routers — left a half-written file and every statistic was lost on the next start.
- The temporary file has one fixed name and is truncated and overwritten by every save, so at most one temporary file can ever exist, even after repeated interrupted saves. It is also removed at startup, and anything occupying its path is cleared instead of making every later save fail.
- A save stuck on stalled storage no longer blocks later saves forever: a new save waits at most 3 seconds, then is skipped and retried on the next interval, and saving resumes once storage recovers. The bound also keeps the shutdown save within procd's 5-second kill timeout. Saves are serialized, so the periodic and shutdown saves never write the temporary file at the same time.
- Log failed periodic saves at the normal level; they used to be visible only with `debug` enabled.
- Write compact JSON instead of indented JSON. Indentation made up about a third of the file, which matters because the file is rewritten every `stats_save_interval` minutes on SD cards and other flash storage. Existing indented files still load.
- Serialize statistics under the lock, but write and flush the file without holding it, so a slow storage card does not delay statistics recording.

## Validation

Regression tests cover compact output, no leftover files across repeated saves, truncation of a longer leftover temporary file, concurrent saves, a stuck save that later recovers, an occupied temporary path, the previous file staying intact when a write fails half-way, file permissions, round-tripping, and cleanup of interrupted temporary files without touching the Badger files that share the directory. Local release checks run `go test -race ./...`, `go vet ./...`, `go build ./...`, 32-bit CI targets and release cross-compilation.

To reduce flash writes further, raise `stats_save_interval` (minutes, default 5); a crash or power cut loses at most that interval of statistics.
