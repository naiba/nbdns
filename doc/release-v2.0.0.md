# v2.0.0

## Breaking changes

- Replace primary/non-primary geographic DNS selection with concurrent upstream queries: the first valid, non-truncated NOERROR or NXDOMAIN reply wins. Failed replies cannot win, and answers are not merged.
- Remove `strategy`, `is_primary`, `blacklist`, IP-country filtering, the China IP list requirement and legacy geographic database code.
- Replace upstream `use_socks` with `proxy_enabled`. SOCKS5 connections require TCP, DoT or DoH.
- v1 configuration files are not compatible. Prepare a v2 configuration before upgrading; no automatic configuration conversion is provided.

## New configuration

- Global `ecs` supports IPv4/IPv6 CIDRs. Each upstream can inherit, override, or disable ECS with an empty string. Client-provided ECS is stripped, and configured prefixes are normalized.
- Cache namespaces isolate different upstream and ECS policies.
- `custom_records` provides exact-name local overrides before filtering, caches and upstream queries. Multiple A/AAAA values and CNAME, TXT, MX, SRV, PTR and NS records are supported. Missing types for a configured name return empty NOERROR replies without upstream queries.
- Add a complete JSON Schema, update configuration examples and English/Chinese documentation, and document `.local` / mDNS limitations.

## Operational notes

- ECS is a location hint, not a guarantee of the nearest CDN or resolver integrity. Choose trusted ECS-capable resolvers.
- Local CNAME records return the alias without recursively resolving its target. Overrides do not automatically generate reverse PTR records or provide mDNS discovery.
- Existing release binaries automatically check for updates at startup and every 40–59 minutes. A published release can therefore be installed automatically; configuration is read on the next process start.
