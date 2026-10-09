package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// fppConnectCPUMinWindow is the shortest interval a CPU percentage is
// measured over, so two polls arriving together never report a noise sample.
const fppConnectCPUMinWindow = time.Second

// fppConnectUtilization is the Utilization object FPP's MultiSync page reads
// from a row's advancedView. A member this host cannot measure is omitted.
type fppConnectUtilization struct {
	CPU    *float64 `json:"CPU,omitempty"`
	Memory *float64 `json:"Memory,omitempty"`
	// Uptime is FPP's own "D days H:M" or "H:M" string, which both the 9.5.3
	// and the 10.0 page render.
	Uptime string `json:"Uptime,omitempty"`
}

// fppConnectHostStats reads this host's CPU, memory and uptime from procRoot.
type fppConnectHostStats struct {
	procRoot string
	now      func() time.Time

	mu        sync.Mutex
	prevAt    time.Time
	prevBusy  uint64
	prevTotal uint64
	havePrev  bool
	cpu       *float64
}

func newFPPConnectHostStats(procRoot string, now func() time.Time) *fppConnectHostStats {
	return &fppConnectHostStats{procRoot: procRoot, now: now}
}

// utilization returns what this host can measure right now, or nil when it
// can measure nothing, so the caller omits the whole object.
func (h *fppConnectHostStats) utilization() *fppConnectUtilization {
	u := fppConnectUtilization{
		CPU:    h.cpuPercent(),
		Memory: h.memoryPercent(),
		Uptime: h.uptime(),
	}
	if u.CPU == nil && u.Memory == nil && u.Uptime == "" {
		return nil
	}
	return &u
}

// cpuPercent is the busy share of CPU time between two reads at least
// fppConnectCPUMinWindow apart. It is nil until a second read exists.
func (h *fppConnectHostStats) cpuPercent() *float64 {
	busy, total, ok := h.readCPUTimes()
	if !ok {
		return nil
	}
	now := h.now()

	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.havePrev {
		h.prevAt, h.prevBusy, h.prevTotal, h.havePrev = now, busy, total, true
		return nil
	}
	if now.Sub(h.prevAt) < fppConnectCPUMinWindow || total <= h.prevTotal || busy < h.prevBusy {
		return h.cpu
	}
	pct := 100 * float64(busy-h.prevBusy) / float64(total-h.prevTotal)
	h.cpu = &pct
	h.prevAt, h.prevBusy, h.prevTotal = now, busy, total
	return h.cpu
}

// readCPUTimes sums the first seven fields of /proc/stat's aggregate line
// (user, nice, system, idle, iowait, irq, softirq), the same seven FPP's own
// get_server_cpu_usage counts; only idle is not busy.
func (h *fppConnectHostStats) readCPUTimes() (busy, total uint64, ok bool) {
	data, err := os.ReadFile(filepath.Join(h.procRoot, "stat"))
	if err != nil {
		return 0, 0, false
	}
	line, _, _ := strings.Cut(string(data), "\n")
	fields := strings.Fields(line)
	if len(fields) < 8 || fields[0] != "cpu" {
		return 0, 0, false
	}
	for i, f := range fields[1:8] {
		v, err := strconv.ParseUint(f, 10, 64)
		if err != nil {
			return 0, 0, false
		}
		total += v
		if i != 3 {
			busy += v
		}
	}
	return busy, total, true
}

func (h *fppConnectHostStats) memoryPercent() *float64 {
	data, err := os.ReadFile(filepath.Join(h.procRoot, "meminfo"))
	if err != nil {
		return nil
	}
	var total, available uint64
	var haveTotal, haveAvailable bool
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		v, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			continue
		}
		switch fields[0] {
		case "MemTotal:":
			total, haveTotal = v, true
		case "MemAvailable:":
			available, haveAvailable = v, true
		}
	}
	if !haveTotal || !haveAvailable || total == 0 || available > total {
		return nil
	}
	pct := 100 * float64(total-available) / float64(total)
	return &pct
}

func (h *fppConnectHostStats) uptime() string {
	data, err := os.ReadFile(filepath.Join(h.procRoot, "uptime"))
	if err != nil {
		return ""
	}
	fields := strings.Fields(string(data))
	if len(fields) == 0 {
		return ""
	}
	seconds, err := strconv.ParseFloat(fields[0], 64)
	if err != nil || seconds < 0 {
		return ""
	}
	return fppConnectFormatUptime(int64(seconds))
}

// fppConnectFormatUptime matches FPP's get_server_uptime: minutes are not
// zero padded, and the days part appears only once a full day has passed.
func fppConnectFormatUptime(seconds int64) string {
	minutes := seconds / 60
	hours := minutes / 60
	days := hours / 24
	hm := fmt.Sprintf("%d:%d", hours%24, minutes%60)
	if days > 0 {
		return fmt.Sprintf("%d days %s", days, hm)
	}
	return hm
}
