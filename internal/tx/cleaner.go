package tx

import (
	"context"
	"time"
)

func StartCleaner(ctx context.Context, c *Coordinator, interval time.Duration) {
	ticker := time.NewTicker(interval)
	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				_, _ = c.ExpireDue()
			}
		}
	}()
}
