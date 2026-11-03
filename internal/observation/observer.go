package observation

import (
	"context"
	"log"
	"time"

	"github.com/lucasmendoncca/OrbMQ/internal/broker"
	"github.com/lucasmendoncca/OrbMQ/internal/server"
	"github.com/lucasmendoncca/OrbMQ/internal/session"
)

type Sources struct {
	Sessions *session.Store
	Broker   *broker.Broker
	Server   *server.Server
}

// Run emits bounded-cardinality aggregate logs until cancellation. Intervals
// must be positive; configuration validation occurs before starting the broker.
func Run(ctx context.Context, sources Sources, summaryInterval, saturationInterval time.Duration, logger *log.Logger) {
	store := sources.Sessions
	summary := time.NewTicker(summaryInterval)
	saturation := time.NewTicker(saturationInterval)
	defer summary.Stop()
	defer saturation.Stop()
	var lastSession, lastGlobal uint64
	lastResources := make(map[string]uint64)
	for {
		select {
		case <-ctx.Done():
			return
		case <-summary.C:
			stats := store.Snapshot()
			logger.Printf("sessions pending_messages=%d pending_bytes=%d inflight_messages=%d oldest_pending_age=%s session_limit_drops=%d global_limit_drops=%d", stats.PendingMessages, stats.PendingBytes, stats.InflightMessages, stats.OldestPendingAge, stats.SessionLimitDrops, stats.GlobalLimitDrops)
			connections, retained := sources.Server.Snapshot(), sources.Broker.Snapshot()
			logger.Printf("security authentication_rejections=%d authorization_rejections=%d", connections.AuthenticationRejections, connections.AuthorizationRejections)
			logger.Printf("resources connections=%d handshakes=%d sessions=%d subscriptions=%d retained_messages=%d retained_bytes=%d expired_sessions=%d", connections.Connections, connections.Handshakes, stats.SessionCount, stats.SubscriptionCount, retained.RetainedMessages, retained.RetainedBytes, stats.ExpiredSessions)
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
			connections, retained := sources.Server.Snapshot(), sources.Broker.Snapshot()
			for reason, total := range map[string]uint64{
				"authentication": connections.AuthenticationRejections, "authorization": connections.AuthorizationRejections,
				"connections": connections.ConnectionRejections, "handshakes": connections.HandshakeRejections,
				"sessions": stats.SessionRejections, "subscriptions": stats.SubscriptionRejections,
				"filters": stats.FilterRejections, "retained": retained.RetainedRejections,
			} {
				if total != lastResources[reason] {
					logger.Printf("resource rejection reason=%s rejected=%d", reason, total-lastResources[reason])
					lastResources[reason] = total
				}
			}
		}
	}
}
