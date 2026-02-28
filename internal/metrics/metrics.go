package metrics

import "github.com/prometheus/client_golang/prometheus"

var (
	ActiveSessions = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "h3ws_proxy_active_sessions",
		Help: "Number of active proxy sessions",
	})
	Accepted = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "h3ws_proxy_accepted_total",
		Help: "Accepted RFC9220 sessions",
	})
	Rejected = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "h3ws_proxy_rejected_total",
		Help: "Rejected requests by reason",
	}, []string{"reason"})
	Errors = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "h3ws_proxy_errors_total",
		Help: "Errors by stage",
	}, []string{"stage"})
	Bytes = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "h3ws_proxy_bytes_total",
		Help: "Bytes forwarded by direction",
	}, []string{"dir"})
	Messages = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "h3ws_proxy_messages_total",
		Help: "Messages forwarded by direction and type",
	}, []string{"dir", "type"})
	Ctrl = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "h3ws_proxy_control_frames_total",
		Help: "Control frames observed",
	}, []string{"type"})
	OversizeDrops = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "h3ws_proxy_oversize_drops_total",
		Help: "Dropped frames/messages due to size limits",
	}, []string{"kind"})
)

func init() {
	prometheus.MustRegister(
		ActiveSessions, Accepted, Rejected, Errors,
		Bytes, Messages, Ctrl, OversizeDrops,
	)
}
