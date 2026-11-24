# v2.3.2 — Compact ECS repair list and footer links

Dashboard refinements for v2.3.0. No configuration migration is required. Existing installations with automatic updates may install this release and restart.

## Changes

- The ECS blackhole repair panel listed all of its up to 50 most recent domains at full height. The list now scrolls inside the panel like the domain rankings, with an "展开全部" button to show every entry and a badge noting that only the 50 most recent domains are kept. The scroll position and expansion survive the automatic refresh.
- The running version, with its release notes link, and the GitHub link now appear only in the footer; the header keeps the refresh countdown, update check and theme switch.

## Validation

Checked in a browser with 50 repaired domains: the panel stays compact, expands and collapses, and keeps its state across refreshes without script errors. Local release checks run `go test -race ./...`, `go vet ./...`, `go build ./...` and the 32-bit CI targets.
