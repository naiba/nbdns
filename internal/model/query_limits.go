package model

const DefaultMaxConcurrentQueries = 64

// DefaultCacheMaxTTL caps cached lifetimes at one day, like Unbound's
// cache-max-ttl. DNS TTLs below it are always honored, so short-TTL records
// such as DDNS names still expire on time.
const DefaultCacheMaxTTL = 86400
