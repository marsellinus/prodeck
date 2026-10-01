package platform

import (
	"math"
	"strings"
	"testing"
)

func TestParseProcStat(t *testing.T) {
	sample := `cpu  100 0 50 800 50 0 0 0 0 0
cpu0 50 0 25 400 25 0 0 0 0 0
intr 12345
`
	got, err := parseProcStat(strings.NewReader(sample))
	if err != nil {
		t.Fatalf("parseProcStat: %v", err)
	}
	// total = 100+0+50+800+50 = 1000; idle = 800+50 = 850; busy = 150.
	if got.total != 1000 || got.busy != 150 {
		t.Errorf("got total=%d busy=%d, want 1000/150", got.total, got.busy)
	}

	if _, err := parseProcStat(strings.NewReader("intr 1\n")); err == nil {
		t.Errorf("missing cpu line: want error")
	}
}

func TestParseMeminfo(t *testing.T) {
	sample := `MemTotal:       16384000 kB
MemFree:         1000000 kB
MemAvailable:    8192000 kB
Buffers:          100000 kB
`
	total, available, ok, err := parseMeminfo(strings.NewReader(sample))
	if err != nil {
		t.Fatalf("parseMeminfo: %v", err)
	}
	if !ok {
		t.Fatalf("MemAvailable should have been found")
	}
	if total != 16384000*1024 || available != 8192000*1024 {
		t.Errorf("got total=%d available=%d", total, available)
	}

	// Old kernel: no MemAvailable.
	_, _, ok, err = parseMeminfo(strings.NewReader("MemTotal: 1000 kB\n"))
	if err != nil {
		t.Fatalf("parseMeminfo without MemAvailable: %v", err)
	}
	if ok {
		t.Errorf("MemAvailable reported present when absent")
	}

	if _, _, _, err := parseMeminfo(strings.NewReader("MemFree: 1 kB\n")); err == nil {
		t.Errorf("missing MemTotal: want error")
	}
}

func TestParseNetDev(t *testing.T) {
	sample := `Inter-|   Receive                                                |  Transmit
 face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs drop fifo colls carrier compressed
    lo: 1000       10    0    0    0     0          0         0     2000       10    0    0    0     0       0          0
  eth0: 5000       50    0    0    0     0          0         0     7000       70    0    0    0     0       0          0
`
	rx, tx, err := parseNetDev(strings.NewReader(sample))
	if err != nil {
		t.Fatalf("parseNetDev: %v", err)
	}
	if rx != 6000 || tx != 9000 {
		t.Errorf("got rx=%d tx=%d, want 6000/9000", rx, tx)
	}
}

func TestParseUptime(t *testing.T) {
	got, err := parseUptime("12345.67 98765.43\n")
	if err != nil {
		t.Fatalf("parseUptime: %v", err)
	}
	if math.Abs(got-12345.67) > 0.001 {
		t.Errorf("got %v, want 12345.67", got)
	}
	if _, err := parseUptime("   \n"); err == nil {
		t.Errorf("empty uptime: want error")
	}
}

func TestParseTopCPU(t *testing.T) {
	sample := `Processes: 500 total
CPU usage: 5.26% user, 10.52% sys, 84.21% idle
CPU usage: 8.00% user, 12.00% sys, 80.00% idle
`
	got, err := parseTopCPU(sample)
	if err != nil {
		t.Fatalf("parseTopCPU: %v", err)
	}
	// The last line wins; busy = 100 - 80 = 20.
	if math.Abs(got-20) > 0.001 {
		t.Errorf("got %v, want 20", got)
	}

	if _, err := parseTopCPU("no cpu line here"); err == nil {
		t.Errorf("no CPU usage line: want error")
	}
}

func TestParseVMStat(t *testing.T) {
	sample := `Mach Virtual Memory Statistics: (page size of 16384 bytes)
Pages free:                          100000.
Pages active:                        200000.
Pages inactive:                      150000.
Pages wired down:                     50000.
Pages occupied by compressor:         25000.
`
	pageSize, pages, err := parseVMStat(sample)
	if err != nil {
		t.Fatalf("parseVMStat: %v", err)
	}
	if pageSize != 16384 {
		t.Errorf("pageSize = %d, want 16384", pageSize)
	}
	// active + wired + compressor = 200000 + 50000 + 25000 = 275000
	if pages != 275000 {
		t.Errorf("pages = %d, want 275000", pages)
	}

	if _, _, err := parseVMStat("nothing useful"); err == nil {
		t.Errorf("no page counts: want error")
	}
}

func TestParseNetstat(t *testing.T) {
	sample := `Name  Mtu   Network       Address            Ipkts Ierrs     Ibytes    Opkts Oerrs     Obytes  Coll
lo0   16384 <Link#1>                        1000     0       5000     1000     0       6000     0
lo0   16384 127          127.0.0.1           1000     -       5000     1000     -       6000     -
en0   1500  <Link#4>    aa:bb:cc:dd:ee:ff   2000     0      10000     3000     0      20000     0
en0   1500  192.168.1.5 192.168.1.5          2000     -      10000     3000     -      20000     -
`
	rx, tx, err := parseNetstat(sample)
	if err != nil {
		t.Fatalf("parseNetstat: %v", err)
	}
	// lo0 counted once (5000/6000), en0 counted once (10000/20000): the second
	// row for each interface must not be added again.
	if rx != 15000 || tx != 26000 {
		t.Errorf("got rx=%d tx=%d, want 15000/26000", rx, tx)
	}

	if _, _, err := parseNetstat("Name Mtu Network\n"); err == nil {
		t.Errorf("header only: want error")
	}
}

func TestParseDF(t *testing.T) {
	sample := `Filesystem   1024-blocks      Used Available Capacity Mounted on
/dev/disk1s5   500000000 250000000 250000000      50%   /
`
	got, err := parseDF(sample)
	if err != nil {
		t.Fatalf("parseDF: %v", err)
	}
	if math.Abs(got-50) > 0.001 {
		t.Errorf("got %v, want 50", got)
	}

	if _, err := parseDF("only a header\n"); err == nil {
		t.Errorf("no data row: want error")
	}
}

func TestParseBoottime(t *testing.T) {
	sample := `{ sec = 1780000000, usec = 123456 } Tue Sep 30 12:00:00 2026`
	got, err := parseBoottime(sample)
	if err != nil {
		t.Fatalf("parseBoottime: %v", err)
	}
	if got != 1780000000 {
		t.Errorf("got %d, want 1780000000", got)
	}

	if _, err := parseBoottime("garbage"); err == nil {
		t.Errorf("garbage: want error")
	}
}
