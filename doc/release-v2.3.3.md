# v2.3.3 — Query types in the ECS repair list

No configuration migration is required. Existing installations with automatic updates may install this release and restart.

## Fix

Some names in the ECS blackhole repair list showed no repaired address. Those repairs were real: the query carrying ECS was REFUSED and the retry without ECS answered NOERROR, but for a query type without addresses, typically the HTTPS queries Apple devices send in large numbers or AAAA queries for names without IPv6. Clients received that empty answer instead of SERVFAIL, yet the dashboard only recorded addresses, so the entry looked like a failed repair.

Each repaired name now records its query types (`query_types` in `/api/stats`), and an entry without addresses reads, for example, "无地址记录（HTTPS 查询）". Entries recorded by earlier versions have no types and explain the likely cause in their tooltip.

## Validation

Tests check that fallback reports carry the query type, including fixes served from the 24-hour memory, that types are merged per name, sorted and persisted, and that the handler passes them to statistics. The list was checked in a browser. Local release checks run `go test -race ./...`, `go vet ./...`, `go build ./...` and the 32-bit CI targets.
