package platform

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// This file parses the text formats the Linux and macOS samplers read.
//
// It has no build tag on purpose. Parsing is the fragile half of metrics
// collection: /proc is stable but `vm_stat`, `top` and `netstat` print for
// humans and have changed shape between releases, so the parsers are separated
// from the I/O that feeds them and are exercised by tests on every platform
// instead of only on the OS that ships them.
//
// Every parser returns an error for input it cannot understand. The sampler
// turns that into "metric absent", which the client renders as "--"; it never
// turns into a zero, which would be a lie.

// procCPUSample is the aggregate CPU counter from /proc/stat, in USER_HZ.
type procCPUSample struct {
	busy  uint64
	total uint64
}

// parseProcStat reads the aggregate "cpu" line.
//
// idle and iowait count as not-busy; everything else counts as work, which is
// what top reports. iowait is counted as idle because the CPU was not doing
// anything, it was waiting.
func parseProcStat(r io.Reader) (procCPUSample, error) {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "cpu ") {
			continue
		}
		fields := strings.Fields(line)[1:]
		var idle, total uint64
		for i, field := range fields {
			v, err := strconv.ParseUint(field, 10, 64)
			if err != nil {
				continue
			}
			total += v
			if i == 3 || i == 4 { // idle, iowait
				idle += v
			}
		}
		if total == 0 {
			break
		}
		return procCPUSample{busy: total - idle, total: total}, nil
	}
	if err := sc.Err(); err != nil {
		return procCPUSample{}, err
	}
	return procCPUSample{}, fmt.Errorf("/proc/stat has no aggregate cpu line")
}

// parseMeminfo returns MemTotal and MemAvailable in bytes. MemAvailable is
// absent on kernels before 3.14, which is reported as ok=false rather than as
// zero so the caller can leave the derived metrics out.
func parseMeminfo(r io.Reader) (total, available uint64, ok bool, err error) {
	var haveTotal, haveAvail bool
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		key, value, found := strings.Cut(sc.Text(), ":")
		if !found {
			continue
		}
		fields := strings.Fields(value)
		if len(fields) == 0 {
			continue
		}
		kb, parseErr := strconv.ParseUint(fields[0], 10, 64)
		if parseErr != nil {
			continue
		}
		switch key {
		case "MemTotal":
			total, haveTotal = kb*1024, true
		case "MemAvailable":
			available, haveAvail = kb*1024, true
		}
	}
	if err := sc.Err(); err != nil {
		return 0, 0, false, err
	}
	if !haveTotal {
		return 0, 0, false, fmt.Errorf("/proc/meminfo has no MemTotal")
	}
	return total, available, haveAvail, nil
}

// parseNetDev sums the receive and transmit byte counters of every interface,
// including the loopback one: the host's own traffic is still traffic.
func parseNetDev(r io.Reader) (rx, tx uint64, err error) {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		_, counters, found := strings.Cut(sc.Text(), ":")
		if !found {
			continue // the two header lines
		}
		fields := strings.Fields(counters)
		if len(fields) < 9 {
			continue
		}
		in, errIn := strconv.ParseUint(fields[0], 10, 64)
		out, errOut := strconv.ParseUint(fields[8], 10, 64)
		if errIn != nil || errOut != nil {
			continue
		}
		rx += in
		tx += out
	}
	return rx, tx, sc.Err()
}

// parseUptime reads the seconds field of /proc/uptime.
func parseUptime(text string) (float64, error) {
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return 0, fmt.Errorf("/proc/uptime is empty")
	}
	seconds, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0, fmt.Errorf("/proc/uptime: %w", err)
	}
	return seconds, nil
}

// parseUint is a small helper for the tools that print one bare number.
func parseUint(text string) (uint64, error) {
	return strconv.ParseUint(text, 10, 64)
}

// parseTopCPU reads the last "CPU usage:" line from `top -l 2`.
//
// top reports cumulative percentages, so two samples are needed and the last
// line is the instantaneous one. Idle is authoritative; user+sys excludes nice
// and interrupt time, so it is used only as a cross-check when it is larger.
func parseTopCPU(text string) (float64, error) {
	var line string
	sc := bufio.NewScanner(strings.NewReader(text))
	for sc.Scan() {
		if strings.HasPrefix(strings.TrimSpace(sc.Text()), "CPU usage:") {
			line = sc.Text()
		}
	}
	if line == "" {
		return 0, fmt.Errorf("top reported no CPU usage line")
	}
	var user, sys, idle float64
	var haveIdle bool
	for _, part := range strings.Split(strings.TrimPrefix(line, "CPU usage:"), ",") {
		fields := strings.Fields(strings.TrimSpace(part))
		if len(fields) < 2 {
			continue
		}
		value, err := strconv.ParseFloat(strings.TrimSuffix(fields[0], "%"), 64)
		if err != nil {
			continue
		}
		switch strings.TrimSuffix(fields[1], "%") {
		case "user":
			user = value
		case "sys":
			sys = value
		case "idle":
			idle, haveIdle = value, true
		}
	}
	if !haveIdle {
		return 0, fmt.Errorf("top reported no idle percentage")
	}
	busy := 100 - idle
	if user+sys > busy {
		busy = user + sys
	}
	return busy, nil
}

// parseVMStat returns the used page count in bytes from vm_stat output.
//
// "Used" is what Activity Monitor calls Memory Used: resident plus wired plus
// compressed. Counting free and inactive pages as used would overstate it;
// counting only resident pages would understate it.
func parseVMStat(text string) (pageSize uint64, pages uint64, err error) {
	pageSize = 4096 // the default; vm_stat prints the real one on its first line
	counts := map[string]uint64{}
	sc := bufio.NewScanner(strings.NewReader(text))
	for sc.Scan() {
		line := sc.Text()
		if _, rest, found := strings.Cut(line, "page size of"); found {
			if fields := strings.Fields(rest); len(fields) > 0 {
				if size, parseErr := strconv.ParseUint(fields[0], 10, 64); parseErr == nil && size > 0 {
					pageSize = size
				}
			}
			continue
		}
		key, value, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		count, parseErr := strconv.ParseUint(strings.Trim(strings.TrimSpace(value), "."), 10, 64)
		if parseErr != nil {
			continue
		}
		counts[strings.TrimSpace(key)] = count
	}
	pages = counts["Pages active"] + counts["Pages wired down"] + counts["Pages occupied by compressor"]
	if pages == 0 {
		return 0, 0, fmt.Errorf("vm_stat reported no page counts")
	}
	return pageSize, pages, nil
}

// parseNetstat sums the octet counters from `netstat -ib`.
//
// netstat prints one line per address on an interface, so the counters are read
// once per interface name: summing every line would count the same octets two
// or three times on a machine with both IPv4 and IPv6.
func parseNetstat(text string) (rx, tx uint64, err error) {
	seen := map[string]bool{}
	sc := bufio.NewScanner(strings.NewReader(text))
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 10 {
			continue // header lines and anything else that is not a row
		}
		name := fields[0]
		if name == "Name" || seen[name] {
			continue
		}
		in, out, ok := netstatOctets(fields)
		if !ok {
			continue
		}
		seen[name] = true
		rx += in
		tx += out
	}
	if len(seen) == 0 {
		return 0, 0, fmt.Errorf("netstat -ib reported no interfaces")
	}
	return rx, tx, nil
}

// netstatOctets finds the receive and transmit byte counts in one netstat row.
//
// The columns are fixed in the header, but the Address field is blank on the
// <Link#n> rows and strings.Fields collapses it, which shifts every later
// column left by one. Counting from the right is therefore tried first, with
// the fixed offsets as the fallback.
func netstatOctets(fields []string) (in, out uint64, ok bool) {
	n := len(fields)
	for _, candidate := range [][2]int{
		{n - 5, n - 2}, // Ibytes, Obytes counting from Coll
		{6, 9},         // Ibytes, Obytes by header position
	} {
		if candidate[0] < 0 || candidate[1] >= n {
			continue
		}
		bytesIn, errIn := strconv.ParseUint(fields[candidate[0]], 10, 64)
		bytesOut, errOut := strconv.ParseUint(fields[candidate[1]], 10, 64)
		if errIn == nil && errOut == nil {
			return bytesIn, bytesOut, true
		}
	}
	return 0, 0, false
}

// parseDF returns the used percentage from `df -P -k`.
func parseDF(text string) (float64, error) {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	if len(lines) < 2 {
		return 0, fmt.Errorf("df returned no data row")
	}
	fields := strings.Fields(lines[len(lines)-1])
	if len(fields) < 3 {
		return 0, fmt.Errorf("df returned an unexpected row %q", lines[len(lines)-1])
	}
	total, err := strconv.ParseUint(fields[1], 10, 64)
	if err != nil || total == 0 {
		return 0, fmt.Errorf("df reported total %q", fields[1])
	}
	used, err := strconv.ParseUint(fields[2], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("df reported used %q", fields[2])
	}
	return float64(used) / float64(total) * 100, nil
}

// parseBoottime extracts the boot timestamp from `sysctl -n kern.boottime`,
// which prints "{ sec = 1780000000, usec = 0 } Tue Sep 30 ...".
func parseBoottime(text string) (int64, error) {
	_, rest, found := strings.Cut(text, "sec =")
	if !found {
		return 0, fmt.Errorf("sysctl kern.boottime returned %q", strings.TrimSpace(text))
	}
	field := strings.TrimSpace(strings.SplitN(rest, ",", 2)[0])
	boot, err := strconv.ParseInt(field, 10, 64)
	if err != nil || boot <= 0 {
		return 0, fmt.Errorf("sysctl kern.boottime reported sec %q", field)
	}
	return boot, nil
}
