//go:build windows

package platform

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Metric names are defined once in metrics_names.go so the three platforms
// cannot drift apart.

// kernel32 is the same handle the input layer resolves; these three entry
// points are not wrapped by x/sys, so they are called through it directly.
var (
	procGetSystemTimes       = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetSystemTimes")
	procGlobalMemoryStatusEx = windows.NewLazySystemDLL("kernel32.dll").NewProc("GlobalMemoryStatusEx")
	procGetTickCount64       = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetTickCount64")
)

// memoryStatusEx mirrors MEMORYSTATUSEX.
type memoryStatusEx struct {
	Length               uint32
	MemoryLoad           uint32
	TotalPhys            uint64
	AvailPhys            uint64
	TotalPageFile        uint64
	AvailPageFile        uint64
	TotalVirtual         uint64
	AvailVirtual         uint64
	AvailExtendedVirtual uint64
}

// cpuSample is the raw GetSystemTimes reading, in 100 ns units.
type cpuSample struct {
	idle   uint64
	kernel uint64
	user   uint64
}

// windowsMetrics samples host health.
//
// CPU, memory and network rates are deltas, so the previous reading has to
// survive between calls. The mutex guards that state; the sampler is called
// from the telemetry loop and, potentially, from a status request at the same
// time.
type windowsMetrics struct {
	Unsupported

	mu  sync.Mutex
	cpu *cpuSample
	net *netSample
}

func newWindowsMetrics() *windowsMetrics { return &windowsMetrics{} }

func (m *windowsMetrics) Available() []string { return allMetricNames }

// Sample collects every metric it can. A metric that cannot be read is left out
// of the map, which the client renders as "--"; only a sampler that produced
// nothing at all reports an error, because that means the host is in a state
// where even a status read fails.
func (m *windowsMetrics) Sample(ctx context.Context) (map[string]float64, error) {
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

	record(sampleMemory(out))
	record(sampleDisk(out))
	record(sampleUptime(out))

	if len(out) == 0 && firstErr != nil {
		return nil, fmt.Errorf("platform: no metric could be sampled: %w", firstErr)
	}
	return out, nil
}

// sampleCPU computes the busy fraction between this call and the previous one.
//
// The first call has no previous reading to compare against, so it reports the
// average since boot: that is a real number rather than a fabricated one, and
// every call after it is the instantaneous rate the telemetry stream wants.
//
// Two calls in the same clock tick produce no delta at all (GetSystemTimes
// advances in 100 ns units and both readings land on the same value), so the
// metric is omitted for that sample rather than reported as zero. Callers
// sampling at the protocol's 250 ms floor never hit this.
func (m *windowsMetrics) sampleCPU(out map[string]float64) error {
	var idle, kernel, user windows.Filetime
	r, _, err := procGetSystemTimes.Call(
		uintptr(unsafe.Pointer(&idle)),
		uintptr(unsafe.Pointer(&kernel)),
		uintptr(unsafe.Pointer(&user)),
	)
	if r == 0 {
		return fmt.Errorf("GetSystemTimes: %w", err)
	}
	cur := cpuSample{
		idle:   filetimeToUint64(idle),
		kernel: filetimeToUint64(kernel),
		user:   filetimeToUint64(user),
	}
	prev := m.cpu
	m.cpu = &cur
	if prev == nil {
		prev = &cpuSample{}
	}
	// Kernel time already includes idle time, so the total is kernel+user and
	// the busy part is that total minus idle.
	dIdle := cur.idle - prev.idle
	dTotal := (cur.kernel - prev.kernel) + (cur.user - prev.user)
	if dTotal == 0 {
		return nil
	}
	busy := dTotal - dIdle
	if dIdle > dTotal {
		// A counter went backwards (rare, but possible across a suspend).
		return nil
	}
	out[metricCPUUsage] = float64(busy) / float64(dTotal) * 100
	return nil
}

// sampleMemory reads physical memory usage.
func sampleMemory(out map[string]float64) error {
	var st memoryStatusEx
	st.Length = uint32(unsafe.Sizeof(st))
	r, _, err := procGlobalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&st)))
	if r == 0 {
		return fmt.Errorf("GlobalMemoryStatusEx: %w", err)
	}
	out[metricMemUsedPct] = float64(st.MemoryLoad)
	out[metricMemTotal] = float64(st.TotalPhys)
	out[metricMemUsed] = float64(st.TotalPhys - st.AvailPhys)
	return nil
}

// sampleDisk reports the used fraction of the system drive.
func sampleDisk(out map[string]float64) error {
	drive := strings.TrimSpace(os.Getenv("SystemDrive"))
	if drive == "" {
		drive = "C:"
	}
	root, err := windows.UTF16PtrFromString(drive + `\`)
	if err != nil {
		return fmt.Errorf("system drive %q: %w", drive, err)
	}
	var freeAvail, total, free uint64
	if err := windows.GetDiskFreeSpaceEx(root, &freeAvail, &total, &free); err != nil {
		return fmt.Errorf("GetDiskFreeSpaceEx %s: %w", drive, err)
	}
	if total == 0 {
		return nil
	}
	out[metricDiskUsed] = float64(total-free) / float64(total) * 100
	return nil
}

// sampleNet reports throughput in bytes per second.
//
// The first call only records the baseline: a rate needs two points in time,
// and reporting the total transferred since boot as "bytes per second" would be
// a wildly wrong number.
func (m *windowsMetrics) sampleNet(out map[string]float64) error {
	var table *windows.MibIfTable2
	if err := windows.GetIfTable2Ex(windows.MibIfTableRaw, &table); err != nil {
		return fmt.Errorf("GetIfTable2Ex: %w", err)
	}
	if table == nil {
		return nil
	}
	defer windows.FreeMibTable(unsafe.Pointer(table))

	var rx, tx uint64
	// The table is a variable-length array whose declared length is one; the
	// allocation behind the pointer holds NumEntries rows.
	rows := unsafe.Slice(&table.Table[0], int(table.NumEntries))
	for i := range rows {
		rx += rows[i].InOctets
		tx += rows[i].OutOctets
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

// sampleUptime reports seconds since boot.
func sampleUptime(out map[string]float64) error {
	r, _, err := procGetTickCount64.Call()
	if r == 0 {
		return fmt.Errorf("GetTickCount64: %w", err)
	}
	out[metricUptime] = float64(r) / 1000
	return nil
}

func filetimeToUint64(ft windows.Filetime) uint64 {
	return uint64(ft.HighDateTime)<<32 | uint64(ft.LowDateTime)
}
