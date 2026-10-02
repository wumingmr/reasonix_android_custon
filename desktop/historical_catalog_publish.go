package main

import (
	"context"
	"reflect"
)

func (a *App) historicalCatalogPublisher(ctx context.Context, positions map[string]int) func([]historicalCatalogEntry) {
	c := &a.historicalImports
	return func(batch []historicalCatalogEntry) {
		c.mu.Lock()
		if c.stopped || ctx.Err() != nil {
			c.mu.Unlock()
			return
		}
		changed := false
		for _, entry := range batch {
			if i, exists := positions[entry.node.Key]; exists {
				if !reflect.DeepEqual(c.catalog[i], entry) {
					c.catalog[i], changed = entry, true
				}
			} else {
				positions[entry.node.Key] = len(c.catalog)
				c.catalog = append(c.catalog, entry)
				changed = true
			}
		}
		if changed {
			c.catalogRevision++
		}
		c.mu.Unlock()
		if changed {
			a.emitProjectTreeChangedEvent()
		}
	}
}
