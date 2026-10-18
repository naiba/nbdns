package handler

import "github.com/naiba/nbdns/internal/model"

// NewBootstrapHandler uses the owner's configured records for upstream hostnames.
func (owner *Handler) NewBootstrapHandler(upstreams []*model.Upstream) *Handler {
	h := NewHandler(false, upstreams, "", owner.logger, nil)
	h.customRecords = owner.customRecords
	// Share the existing cache instance, not a second Badger handle for the same
	// directory. Keep policy-based keys: ECS/upstream differences can change
	// answers even when both resolvers are trusted. Only the owner closes it.
	h.builtInCache = owner.builtInCache
	h.borrowedCache = true
	return h
}
