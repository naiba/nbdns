# v2.5.1 — Simpler dashboard and complete release archives

Dashboard and packaging changes only; DNS behavior is unchanged. No configuration migration is required. Existing installations with automatic updates may install this release and restart.

## Dashboard

- The "展开全部" buttons sat in card headings and overflowed cards whose subtitles were long. Long lists now show their first 10 rows with a "显示全部 N 条 / 收起" toggle below the table; the choice survives the automatic refresh, and lists of 10 rows or fewer show no toggle.
- Cards share one neutral style without colored borders, title dots or "Top 50" badges. Explanations moved from subtitles and footnotes into heading tooltips, and card titles are shorter, for example "上游" and "ECS 修复".
- Failure reasons read as short labels such as "dns.example 失败 · 退避中", with details in the tooltip.
- Empty filter subscription tables are replaced by a single line.
- On phones the upstream table hides the average latency and diagnosis results show upstream hosts, so neither table scrolls sideways.
- The footer is one line: runtime status and "NbDNS vX.Y.Z", linking to the project and to the running version's release notes.

## Packaging

Release archives listed release notes one by one and stopped at v2.0.5. They now include every `doc/*.md`.

## Validation

The dashboard was checked in a browser at 1280, 1000 and 390 pixels with full rankings and 50 ECS repairs: no element leaves its card, no table scrolls sideways, list toggles keep their state across refreshes, and the dark theme renders correctly. A local GoReleaser snapshot confirmed the archive contents. Local release checks run `go test -race ./...`, `go vet ./...`, `go build ./...` and the 32-bit CI targets.
