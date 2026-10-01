//go:build linux

package platform

import (
	"context"
	"fmt"
	"os"
	"sync"
	"syscall"
	"time"
)

// linuxMetrics samples host health from /proc and statfs.
//
// /proc is the interface the kernel documents for exactly this; reading it
// needs no privileges and no daemon, and a container that does not mount it
// simply omits those metrics. The text parsing itself lives in metrics_parse.go
// so it can be tested on any platform.
type linuxMetrics struct {
	Unsupported

	mu  sync.Mutex
	cpu *procCPUSample
	net *netSample
}

func (m *linuxMetrics) Available() []string { return allMetricNames }

// Sample collects every metric it can; one that cannot be read is omitted
// rather than failing the whole sample, because the client renders a missing
// metric as "--" and the rest of the numbers are still useful.
func (m *linuxMetrics) Sample(ctx context.Context) (map[string]float64, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	out := make(map[string]float64, len(allMetricNames))
	var firstErr error
	record := func(err error) {
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}

	m.mu.Lock()
	record(m.sampleCPU(out))
	record(m.sampleNet(out))
	m.mu.Unlock()

	record(sampleMeminfo(out))
	record(sampleDiskUsage(out))
	record(sampleUptime(out))

	if len(out) == 0 && firstErr != nil {
		return nil, fmt.Errorf("platform: no metric could be sampled: %w", firstErr)
	}
	return out, nil
}

// sampleCPU computes the busy fraction between this call and the previous one.
//
// The first call reports the average since boot, because a rate needs two
// readings; that is a real measurement rather than a fabricated one. Two calls
// within the same kernel tick (10 ms by default) have no delta, so the metric
// is omitted for that sample rather than reported as zero.
func (m *linuxMetrics) sampleCPU(out map[string]float64) error {
	f, err := os.Open("/proc/stat")
	if err != nil {
		return err
	}
	cur, err := parseProcStat(f)
	f.Close()
	if err != nil {
		return err
	}

	prev := m.cpu
	m.cpu = &cur
	if prev == nil {
		prev = &procCPUSample{}
	}
	dTotal := cur.total - prev.total
	dBusy := cur.busy - prev.busy
	if dTotal == 0 || dBusy > dTotal {
		return nil
	}
	out[metricCPUUsage] = float64(dBusy) / float64(dTotal) * 100
	return nil
}

// sampleMeminfo reads physical memory usage.
func sampleMeminfo(out map[string]float64) error {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return err
	}
	defer f.Close()
	total, available, haveAvailable, err := parseMeminfo(f)
	if err != nil {
		return err
	}
	out[metricMemTotal] = float64(total)
	if !haveAvailable {
		// MemAvailable is missing on kernels before 3.14; the total is still
		// certain, so it is reported and the derived values are left out
		// rather than guessed.
		return nil
	}
	used := total - available
	out[metricMemUsed] = float64(used)
	out[metricMemUsedPct] = float64(used) / float64(total) * 100
	return nil
}

// sampleNet reports throughput in bytes per second.
//
// The first call only records the baseline: a rate needs two points in time.
func (m *linuxMetrics) sampleNet(out map[string]float64) error {
	f, err := os.Open("/proc/net/dev")
	if err != nil {
		return err
	}
	rx, tx, err := parseNetDev(f)
	f.Close()
	if err != nil {
		return err
	}
	now := time.Now()
	prev := m.net
	m.net = &netSample{rx: rx, tx: tx, when: now}
	if prev == nil {
		return nil
	}
	elapsed := now.Sub(prev.when).Seconds()
	if elapsed <= 0 {
		return nil
	}
	if rx >= prev.rx {
		out[metricNetRx] = float64(rx-prev.rx) / elapsed
	}
	if tx >= prev.tx {
		out[metricNetTx] = float64(tx-prev.tx) / elapsed
	}
	return nil
}

// sampleDiskUsage reports the used fraction of the root filesystem.
func sampleDiskUsage(out map[string]float64) error {
	var st syscall.Statfs_t
	if err := syscall.Statfs("/", &st); err != nil {
		return fmt.Errorf("statfs /: %w", err)
	}
	if st.Blocks == 0 {
		return nil
	}
	// Free space is Bavail, not Bfree: the blocks reserved for root are not
	// available to the user, so counting them as free would understate usage.
	free := st.Bavail
	if free > st.Blocks {
		free = st.Blocks
	}
	out[metricDiskUsed] = float64(st.Blocks-free) / float64(st.Blocks) * 100
	return nil
}

// sampleUptime reports seconds since boot.
func sampleUptime(out map[string]float64) error {
	data, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return err
	}
	seconds, err := parseUptime(string(data))
	if err != nil {
		return err
	}
	out[metricUptime] = seconds
	return nil
}
