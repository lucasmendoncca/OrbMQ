package observation

import (
	"context"
	"log"
	"time"

	"github.com/lucasmendoncca/OrbMQ/internal/session"
)

// Run emits bounded-cardinality aggregate logs until cancellation. Intervals
// must be positive; configuration validation occurs before starting the broker.
func Run(ctx context.Context, store *session.Store, summaryInterval, saturationInterval time.Duration, logger *log.Logger) {
	summary := time.NewTicker(summaryInterval)
	saturation := time.NewTicker(saturationInterval)
	defer summary.Stop()
	defer saturation.Stop()
	var lastSession, lastGlobal uint64
	for {
		select {
		case <-ctx.Done():
			return
		case <-summary.C:
			stats := store.Snapshot()
			logger.Printf("sessions pending_messages=%d pending_bytes=%d inflight_messages=%d oldest_pending_age=%s session_limit_drops=%d global_limit_drops=%d", stats.PendingMessages, stats.PendingBytes, stats.InflightMessages, stats.OldestPendingAge, stats.SessionLimitDrops, stats.GlobalLimitDrops)
		case <-saturation.C:
			stats := store.Snapshot()
			if stats.SessionLimitDrops != lastSession {
				logger.Printf("session saturation reason=session_limit dropped=%d", stats.SessionLimitDrops-lastSession)
				lastSession = stats.SessionLimitDrops
			}
			if stats.GlobalLimitDrops != lastGlobal {
				logger.Printf("session saturation reason=global_limit dropped=%d", stats.GlobalLimitDrops-lastGlobal)
				lastGlobal = stats.GlobalLimitDrops
			}
		}
	}
}
