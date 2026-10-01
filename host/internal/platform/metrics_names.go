package platform

import "time"

// Metric names reported by the OS implementations. They live in a file without
// a build tag so that all three platforms cannot drift apart: the client binds
// a telemetry button to a name, and a name that only exists on Windows would
// render as "--" forever on Linux for no visible reason.
const (
	metricCPUUsage   = "cpu.usage"
	metricMemUsedPct = "mem.used_pct"
	metricMemTotal   = "mem.total_bytes"
	metricMemUsed    = "mem.used_bytes"
	metricDiskUsed   = "disk.used_pct"
	metricNetRx      = "net.rx_bps"
	metricNetTx      = "net.tx_bps"
	metricUptime     = "uptime_s"
)

// allMetricNames is the full milestone-1 set, in the order Available() reports.
// An implementation lists it and omits from Sample whatever it cannot read.
var allMetricNames = []string{
	metricCPUUsage,
	metricMemUsedPct,
	metricMemTotal,
	metricMemUsed,
	metricDiskUsed,
	metricNetRx,
	metricNetTx,
	metricUptime,
}

// netSample is one reading of the interface octet counters. Throughput is a
// delta, so the previous reading has to survive between calls; the type is
// shared because the arithmetic is the same on every OS.
type netSample struct {
	rx   uint64
	tx   uint64
	when time.Time
}
