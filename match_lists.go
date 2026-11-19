package main

import (
	"context"
	"log"
	"time"

	"github.com/naiba/nbdns/internal/filter"
	"github.com/naiba/nbdns/internal/model"
)

// matchLists owns the domain list subscriptions of all upstreams. An entry
// used by several upstreams gets one manager: one download, one cache file.
type matchLists struct {
	managers []*filter.Manager
}

func loadMatchLists(dataPath string, upstreams []*model.Upstream) (*matchLists, error) {
	lists := &matchLists{}
	byEntry := make(map[string]*filter.Manager)
	for _, up := range upstreams {
		if len(up.MatchLists) == 0 {
			continue
		}
		sets := make([]model.DomainSet, 0, len(up.MatchLists))
		for _, entry := range up.MatchLists {
			manager, ok := byEntry[entry]
			if !ok {
				var err error
				if manager, err = filter.NewDomainListManager(dataPath, []string{entry}, nil); err != nil {
					return nil, err
				}
				// Cached copies only; downloads run in the background after DNS starts.
				if err := manager.LoadInitial(); err != nil {
					log.Printf("上游域名列表初始加载失败（将后台重试）: %v", err)
				}
				byEntry[entry] = manager
				lists.managers = append(lists.managers, manager)
			}
			sets = append(sets, manager)
		}
		up.SetMatchLists(sets...)
	}
	return lists, nil
}

// Run refreshes every list on the interval until ctx ends.
func (l *matchLists) Run(ctx context.Context, interval time.Duration) {
	for _, manager := range l.managers {
		go manager.Run(ctx, interval, func(err error) {
			if err != nil {
				log.Printf("上游域名列表更新失败（继续使用缓存）: %v", err)
			}
		})
	}
}

// Snapshot lists the status of every subscription for the dashboard.
func (l *matchLists) Snapshot() []filter.SourceStatus {
	sources := []filter.SourceStatus{}
	for _, manager := range l.managers {
		sources = append(sources, manager.Snapshot().Sources...)
	}
	return sources
}
