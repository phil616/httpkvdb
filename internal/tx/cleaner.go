package tx

import (
	"context"
	"log"
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
				if _, err := c.ExpireDue(); err != nil {
					log.Printf("tx_cleanup_error")
				}
			}
		}
	}()
}
