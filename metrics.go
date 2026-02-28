package main

import "github.com/prometheus/client_golang/prometheus"

var (
	mActiveSessions = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "h3ws_proxy_active_sessions",
		Help: "Number of active proxy sessions",
	})
	mAccepted = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "h3ws_proxy_accepted_total",
		Help: "Accepted RFC9220 sessions",
	})
	mRejected = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "h3ws_proxy_rejected_total",
		Help: "Rejected requests by reason",
	}, []string{"reason"})
	mErrors = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "h3ws_proxy_errors_total",
		Help: "Errors by stage",
	}, []string{"stage"})
	mBytes = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "h3ws_proxy_bytes_total",
		Help: "Bytes forwarded by direction",
	}, []string{"dir"})
	mMessages = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "h3ws_proxy_messages_total",
		Help: "Messages forwarded by direction and type",
	}, []string{"dir", "type"})
	mCtrl = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "h3ws_proxy_control_frames_total",
		Help: "Control frames observed",
	}, []string{"type"})
	mOversizeDrops = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "h3ws_proxy_oversize_drops_total",
		Help: "Dropped frames/messages due to size limits",
	}, []string{"kind"})
)

func init() {
	prometheus.MustRegister(
		mActiveSessions, mAccepted, mRejected, mErrors,
		mBytes, mMessages, mCtrl, mOversizeDrops,
	)
}
