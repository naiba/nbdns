# v2.3.1 — Upstream latency for TCP/DoT and configuration schema

Fixes for v2.3.0. No configuration migration is required. Existing installations with automatic updates may install this release and restart.

## Fixes

- The dashboard showed TCP and DNS-over-TLS upstreams with an average latency of about 0.001 ms: pooled TCP/DoT exchanges report no duration, and v2.3.0 used that value. Latency is now measured around each upstream exchange for every protocol, including the ECS retry when one happens. Latency samples recorded by v2.3.0 for TCP/DoT upstreams age out of the 24-hour window.
- `data/config.schema.json` now describes `web_auth`, `web_allowed_hosts` and `client_name_server`; the schema forbids unknown properties, so editors flagged these v2.3.0 settings as errors although nbdns accepted them.

## Validation

A regression test measures latency through a TCP upstream that answers after 30 ms; another checks that every configuration field appears in the schema. Local release checks run `go test -race ./...`, `go vet ./...`, `go build ./...` and the 32-bit CI targets.
