//go:build darwin

package platform

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

// metricsCommandTimeout bounds each sampling tool. A status request must not
// hang because a helper is wedged; a metric that does not answer in time is
// omitted like any other unavailable metric.
const metricsCommandTimeout = 5 * time.Second

// darwinMetrics samples host health from the system's own reporting tools.
//
// macOS has no /proc: the counters live behind sysctl and the command-line
// tools that wrap the Mach APIs. The parsing lives in metrics_parse.go, so the
// part that is most likely to break on a new macOS release is tested on every
// platform instead of only where it runs.
type darwinMetrics struct {
	Unsupported

	mu  sync.Mutex
	net *netSample
}

func (m *darwinMetrics) Available() []string { return allMetricNames }

func (m *darwinMetrics) Sample(ctx context.Context) (map[string]float64, error) {
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

	record(sampleDarwinCPU(ctx, out))
	record(sampleDarwinMemory(ctx, out))

	m.mu.Lock()
	record(m.sampleNet(ctx, out))
	m.mu.Unlock()

	record(sampleDarwinDisk(ctx, out))
	record(sampleDarwinUptime(ctx, out))

	if len(out) == 0 && firstErr != nil {
		return nil, fmt.Errorf("platform: no metric could be sampled: %w", firstErr)
	}
	return out, nil
}

// sampleDarwinCPU reads the aggregate CPU percentages from top.
//
// top needs two samples before it reports a rate, so it is asked for exactly
// two; that costs about a second, which is why the protocol's telemetry
// interval has a 250 ms floor.
//
// A note on the tool choice, because it is not the obvious one: `sysctl -n
// hw.ncpu` reports how many cores exist, not how busy they are, so it cannot
// produce cpu.usage. The only other source is host_processor_info from the Mach
// API, which has no Go binding here. `top -l 2` is therefore the honest way to
// get a real percentage; it is a documented interface and its output is parsed
// defensively above.
func sampleDarwinCPU(ctx context.Context, out map[string]float64) error {
	text, err := runMetricsTool(ctx, "top", "-l", "2", "-n", "0", "-s", "1")
	if err != nil {
		return err
	}
	busy, err := parseTopCPU(text)
	if err != nil {
		return err
	}
	out[metricCPUUsage] = busy
	return nil
}

// sampleDarwinMemory reads total memory from hw.memsize and the page counts
// from vm_stat.
func sampleDarwinMemory(ctx context.Context, out map[string]float64) error {
	memsizeText, err := runMetricsTool(ctx, "sysctl", "-n", "hw.memsize")
	if err != nil {
		return err
	}
	total, err := parseUint(strings.TrimSpace(memsizeText))
	if err != nil || total == 0 {
		return fmt.Errorf("sysctl hw.memsize returned %q", strings.TrimSpace(memsizeText))
	}

	vmText, err := runMetricsTool(ctx, "vm_stat")
	if err != nil {
		return err
	}
	pageSize, pages, err := parseVMStat(vmText)
	if err != nil {
		return err
	}
	used := pages * pageSize
	if used > total {
		used = total
	}
	out[metricMemTotal] = float64(total)
	out[metricMemUsed] = float64(used)
	out[metricMemUsedPct] = float64(used) / float64(total) * 100
	return nil
}

// sampleNet reports throughput in bytes per second from netstat -ib.
func (m *darwinMetrics) sampleNet(ctx context.Context, out map[string]float64) error {
	text, err := runMetricsTool(ctx, "netstat", "-ib")
	if err != nil {
		return err
	}
	rx, tx, err := parseNetstat(text)
	if err != nil {
		return err
	}

	now := time.Now()
	prev := m.net
	m.net = &netSample{rx: rx, tx: tx, when: now}
	if prev == nil {
		// A rate needs two readings; the first call only records the baseline.
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

// sampleDarwinDisk reports the used fraction of the root filesystem.
func sampleDarwinDisk(ctx context.Context, out map[string]float64) error {
	text, err := runMetricsTool(ctx, "df", "-P", "-k", "/")
	if err != nil {
		return err
	}
	used, err := parseDF(text)
	if err != nil {
		return err
	}
	out[metricDiskUsed] = used
	return nil
}

// sampleDarwinUptime reports seconds since boot from kern.boottime.
func sampleDarwinUptime(ctx context.Context, out map[string]float64) error {
	text, err := runMetricsTool(ctx, "sysctl", "-n", "kern.boottime")
	if err != nil {
		return err
	}
	boot, err := parseBoottime(text)
	if err != nil {
		return err
	}
	out[metricUptime] = time.Since(time.Unix(boot, 0)).Seconds()
	return nil
}

// runMetricsTool runs one sampling tool and returns its stdout.
func runMetricsTool(ctx context.Context, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, metricsCommandTimeout)
	defer cancel()
	cmd := CommandContext(ctx, name, args...)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("%s: %w", name, err)
	}
	return string(out), nil
}
