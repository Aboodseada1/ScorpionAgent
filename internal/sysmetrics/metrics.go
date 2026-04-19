// Package sysmetrics reads live host + service telemetry (CPU, memory, disk,
// network, per-unit systemd state) using /proc and systemctl. Pure stdlib.
package sysmetrics

import (
	"bufio"
	"bytes"
	"context"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

type CPUSnap struct {
	UsedPercent  float64 `json:"used_percent"`
	Cores        int     `json:"cores"`
	Load1        float64 `json:"load1"`
	Load5        float64 `json:"load5"`
	Load15       float64 `json:"load15"`
}

type MemSnap struct {
	TotalMB     float64 `json:"total_mb"`
	UsedMB      float64 `json:"used_mb"`
	AvailableMB float64 `json:"available_mb"`
	UsedPercent float64 `json:"used_percent"`
	SwapTotalMB float64 `json:"swap_total_mb"`
	SwapUsedMB  float64 `json:"swap_used_mb"`
}

type DiskSnap struct {
	Mount       string  `json:"mount"`
	TotalGB     float64 `json:"total_gb"`
	UsedGB      float64 `json:"used_gb"`
	FreeGB      float64 `json:"free_gb"`
	UsedPercent float64 `json:"used_percent"`
}

type NetSnap struct {
	Iface    string  `json:"iface"`
	RxKBps   float64 `json:"rx_kbps"`
	TxKBps   float64 `json:"tx_kbps"`
	RxTotalGB float64 `json:"rx_total_gb"`
	TxTotalGB float64 `json:"tx_total_gb"`
}

type GPUSnap struct {
	Name          string  `json:"name"`
	UtilPercent   float64 `json:"util_percent"`
	MemUsedMB     float64 `json:"mem_used_mb"`
	MemTotalMB    float64 `json:"mem_total_mb"`
	TemperatureC  float64 `json:"temperature_c"`
}

type ServiceSnap struct {
	Name        string  `json:"name"`
	Active      string  `json:"active"`       // active, inactive, failed, activating
	SubState    string  `json:"sub_state"`    // running, dead, exited
	PID         int     `json:"pid"`
	MemoryMB    float64 `json:"memory_mb"`
	CPUNsec     uint64  `json:"cpu_nsec"`
	CPUPercent  float64 `json:"cpu_percent"`
	UptimeSec   int64   `json:"uptime_sec"`
	Description string  `json:"description"`
}

type Snapshot struct {
	TS       int64         `json:"ts"`
	Uptime   int64         `json:"uptime_sec"`
	Hostname string        `json:"hostname"`
	Kernel   string        `json:"kernel"`
	CPU      CPUSnap       `json:"cpu"`
	Memory   MemSnap       `json:"memory"`
	Disk     DiskSnap      `json:"disk"`
	Net      NetSnap       `json:"net"`
	GPU      *GPUSnap      `json:"gpu,omitempty"`
	Services []ServiceSnap `json:"services"`
}

// Collector samples at a fixed cadence and broadcasts Snapshot to subscribers.
type Collector struct {
	mu          sync.Mutex
	last        Snapshot
	cpuPrev     cpuStat
	netPrev     map[string]netStat
	netPrevTime time.Time
	subs        map[int]chan Snapshot
	nextSub     int

	servicesCfg []string
	svcPrev     map[string]svcCPU
	mounts      []string
}

type cpuStat struct {
	user, nice, system, idle, iowait, irq, softirq, steal uint64
}

type netStat struct {
	rxBytes, txBytes uint64
}

type svcCPU struct {
	nsec uint64
	at   time.Time
}

// NewCollector. services is a list of systemd unit names (without .service) to watch.
// mounts is a list of mountpoints to report disk usage for (e.g. ["/"]).
func NewCollector(services []string, mounts []string) *Collector {
	if len(mounts) == 0 {
		mounts = []string{"/"}
	}
	return &Collector{
		subs:        map[int]chan Snapshot{},
		netPrev:     map[string]netStat{},
		servicesCfg: services,
		svcPrev:     map[string]svcCPU{},
		mounts:      mounts,
	}
}

// Run blocks until ctx is done, sampling every interval.
func (c *Collector) Run(ctx context.Context, interval time.Duration) {
	tick := time.NewTicker(interval)
	defer tick.Stop()
	// prime
	_ = c.sample()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			snap := c.sample()
			c.fanout(snap)
		}
	}
}

// Snapshot returns the latest cached snapshot (non-blocking).
func (c *Collector) Snapshot() Snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.last
}

// Subscribe returns a channel that receives every new Snapshot.
// Close the channel via the returned cancel func when done.
func (c *Collector) Subscribe() (<-chan Snapshot, func()) {
	c.mu.Lock()
	id := c.nextSub
	c.nextSub++
	ch := make(chan Snapshot, 4)
	c.subs[id] = ch
	// Immediately hand over the latest snapshot so the subscriber doesn't wait.
	if c.last.TS != 0 {
		select {
		case ch <- c.last:
		default:
		}
	}
	c.mu.Unlock()
	return ch, func() {
		c.mu.Lock()
		if sub, ok := c.subs[id]; ok {
			delete(c.subs, id)
			close(sub)
		}
		c.mu.Unlock()
	}
}

func (c *Collector) fanout(s Snapshot) {
	c.mu.Lock()
	for _, ch := range c.subs {
		select {
		case ch <- s:
		default:
		}
	}
	c.mu.Unlock()
}

func (c *Collector) sample() Snapshot {
	now := time.Now()
	s := Snapshot{
		TS:       now.Unix(),
		Hostname: hostname(),
		Kernel:   kernel(),
		Uptime:   readUptime(),
	}
	s.CPU = c.readCPU()
	s.Memory = readMem()
	s.Disk = readDisk(c.mounts[0])
	s.Net = c.readNet()
	if gpu := readGPU(); gpu != nil {
		s.GPU = gpu
	}
	s.Services = c.readServices()

	c.mu.Lock()
	c.last = s
	c.mu.Unlock()
	return s
}

// ---------- readers ----------

func hostname() string {
	h, _ := os.Hostname()
	return h
}

func kernel() string {
	b, err := os.ReadFile("/proc/sys/kernel/osrelease")
	if err != nil {
		return runtime.GOOS
	}
	return strings.TrimSpace(string(b))
}

func readUptime() int64 {
	b, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0
	}
	fields := strings.Fields(string(b))
	if len(fields) == 0 {
		return 0
	}
	f, _ := strconv.ParseFloat(fields[0], 64)
	return int64(f)
}

func (c *Collector) readCPU() CPUSnap {
	out := CPUSnap{Cores: runtime.NumCPU()}
	if b, err := os.ReadFile("/proc/loadavg"); err == nil {
		fields := strings.Fields(string(b))
		if len(fields) >= 3 {
			out.Load1, _ = strconv.ParseFloat(fields[0], 64)
			out.Load5, _ = strconv.ParseFloat(fields[1], 64)
			out.Load15, _ = strconv.ParseFloat(fields[2], 64)
		}
	}
	cur, ok := readCPUStat()
	if !ok {
		return out
	}
	c.mu.Lock()
	prev := c.cpuPrev
	c.cpuPrev = cur
	c.mu.Unlock()
	if prev.idle == 0 && prev.user == 0 {
		return out
	}
	totalCur := cur.user + cur.nice + cur.system + cur.idle + cur.iowait + cur.irq + cur.softirq + cur.steal
	totalPrev := prev.user + prev.nice + prev.system + prev.idle + prev.iowait + prev.irq + prev.softirq + prev.steal
	idleDelta := float64(cur.idle+cur.iowait) - float64(prev.idle+prev.iowait)
	totalDelta := float64(totalCur) - float64(totalPrev)
	if totalDelta <= 0 {
		return out
	}
	out.UsedPercent = (1 - idleDelta/totalDelta) * 100.0
	if out.UsedPercent < 0 {
		out.UsedPercent = 0
	}
	if out.UsedPercent > 100 {
		out.UsedPercent = 100
	}
	return out
}

func readCPUStat() (cpuStat, bool) {
	f, err := os.Open("/proc/stat")
	if err != nil {
		return cpuStat{}, false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	if !sc.Scan() {
		return cpuStat{}, false
	}
	fields := strings.Fields(sc.Text())
	if len(fields) < 8 || fields[0] != "cpu" {
		return cpuStat{}, false
	}
	parseU := func(i int) uint64 { n, _ := strconv.ParseUint(fields[i], 10, 64); return n }
	return cpuStat{
		user: parseU(1), nice: parseU(2), system: parseU(3),
		idle: parseU(4), iowait: parseU(5), irq: parseU(6),
		softirq: parseU(7), steal: parseU(8),
	}, true
}

func readMem() MemSnap {
	out := MemSnap{}
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return out
	}
	kv := map[string]uint64{}
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		line := sc.Text()
		idx := strings.Index(line, ":")
		if idx < 0 {
			continue
		}
		k := line[:idx]
		v := strings.Fields(line[idx+1:])
		if len(v) == 0 {
			continue
		}
		n, err := strconv.ParseUint(v[0], 10, 64)
		if err != nil {
			continue
		}
		kv[k] = n
	}
	out.TotalMB = float64(kv["MemTotal"]) / 1024.0
	out.AvailableMB = float64(kv["MemAvailable"]) / 1024.0
	out.UsedMB = out.TotalMB - out.AvailableMB
	if out.TotalMB > 0 {
		out.UsedPercent = 100 * out.UsedMB / out.TotalMB
	}
	out.SwapTotalMB = float64(kv["SwapTotal"]) / 1024.0
	out.SwapUsedMB = float64(kv["SwapTotal"]-kv["SwapFree"]) / 1024.0
	return out
}

func readDisk(mount string) DiskSnap {
	out := DiskSnap{Mount: mount}
	var s syscall.Statfs_t
	if err := syscall.Statfs(mount, &s); err != nil {
		return out
	}
	blockSize := uint64(s.Bsize)
	total := s.Blocks * blockSize
	free := s.Bavail * blockSize
	used := total - (s.Bfree * blockSize)
	gb := 1 << 30
	out.TotalGB = float64(total) / float64(gb)
	out.FreeGB = float64(free) / float64(gb)
	out.UsedGB = float64(used) / float64(gb)
	if out.TotalGB > 0 {
		out.UsedPercent = 100 * out.UsedGB / out.TotalGB
	}
	return out
}

func (c *Collector) readNet() NetSnap {
	out := NetSnap{}
	b, err := os.ReadFile("/proc/net/dev")
	if err != nil {
		return out
	}
	now := time.Now()
	var bestIface string
	var bestBytes uint64
	var rxTot, txTot uint64
	cur := map[string]netStat{}
	sc := bufio.NewScanner(bytes.NewReader(b))
	// skip 2 header lines
	sc.Scan()
	sc.Scan()
	for sc.Scan() {
		line := sc.Text()
		colon := strings.Index(line, ":")
		if colon < 0 {
			continue
		}
		iface := strings.TrimSpace(line[:colon])
		if iface == "lo" || strings.HasPrefix(iface, "docker") || strings.HasPrefix(iface, "veth") || strings.HasPrefix(iface, "br-") {
			continue
		}
		fields := strings.Fields(line[colon+1:])
		if len(fields) < 9 {
			continue
		}
		rx, _ := strconv.ParseUint(fields[0], 10, 64)
		tx, _ := strconv.ParseUint(fields[8], 10, 64)
		cur[iface] = netStat{rxBytes: rx, txBytes: tx}
		rxTot += rx
		txTot += tx
		if rx+tx > bestBytes {
			bestBytes = rx + tx
			bestIface = iface
		}
	}
	out.Iface = bestIface
	out.RxTotalGB = float64(rxTot) / (1 << 30)
	out.TxTotalGB = float64(txTot) / (1 << 30)

	c.mu.Lock()
	prev := c.netPrev
	prevT := c.netPrevTime
	c.netPrev = cur
	c.netPrevTime = now
	c.mu.Unlock()
	if bestIface == "" || prev == nil || prevT.IsZero() {
		return out
	}
	p, ok := prev[bestIface]
	if !ok {
		return out
	}
	dt := now.Sub(prevT).Seconds()
	if dt <= 0 {
		return out
	}
	c2 := cur[bestIface]
	drx := float64(c2.rxBytes - p.rxBytes)
	dtx := float64(c2.txBytes - p.txBytes)
	out.RxKBps = drx / 1024 / dt
	out.TxKBps = dtx / 1024 / dt
	return out
}

func readGPU() *GPUSnap {
	if _, err := exec.LookPath("nvidia-smi"); err != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "nvidia-smi",
		"--query-gpu=name,utilization.gpu,memory.used,memory.total,temperature.gpu",
		"--format=csv,noheader,nounits")
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	line := strings.TrimSpace(strings.Split(string(out), "\n")[0])
	if line == "" {
		return nil
	}
	parts := strings.Split(line, ",")
	if len(parts) < 5 {
		return nil
	}
	g := &GPUSnap{Name: strings.TrimSpace(parts[0])}
	g.UtilPercent, _ = strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
	g.MemUsedMB, _ = strconv.ParseFloat(strings.TrimSpace(parts[2]), 64)
	g.MemTotalMB, _ = strconv.ParseFloat(strings.TrimSpace(parts[3]), 64)
	g.TemperatureC, _ = strconv.ParseFloat(strings.TrimSpace(parts[4]), 64)
	return g
}

func (c *Collector) readServices() []ServiceSnap {
	out := make([]ServiceSnap, 0, len(c.servicesCfg))
	now := time.Now()
	for _, name := range c.servicesCfg {
		s := ServiceSnap{Name: name}
		props := systemctlShow(name)
		s.Description = props["Description"]
		s.Active = props["ActiveState"]
		s.SubState = props["SubState"]
		if p := props["MainPID"]; p != "" {
			s.PID, _ = strconv.Atoi(p)
		}
		if m := props["MemoryCurrent"]; m != "" && m != "[not set]" {
			n, _ := strconv.ParseUint(m, 10, 64)
			s.MemoryMB = float64(n) / (1024 * 1024)
		}
		if cu := props["CPUUsageNSec"]; cu != "" && cu != "[not set]" {
			n, _ := strconv.ParseUint(cu, 10, 64)
			s.CPUNsec = n
			c.mu.Lock()
			prev, ok := c.svcPrev[name]
			c.svcPrev[name] = svcCPU{nsec: n, at: now}
			c.mu.Unlock()
			if ok && now.After(prev.at) {
				dt := now.Sub(prev.at).Nanoseconds()
				if dt > 0 && n >= prev.nsec {
					dn := float64(n - prev.nsec)
					s.CPUPercent = 100.0 * dn / float64(dt)
				}
			}
		}
		if ts := props["ActiveEnterTimestampMonotonic"]; ts != "" {
			n, _ := strconv.ParseUint(ts, 10, 64)
			// Monotonic since boot in microseconds; convert to uptime delta.
			bootUp := readUptime()
			if bootUp > 0 && n > 0 {
				elapsed := bootUp - int64(n/1_000_000)
				if elapsed > 0 {
					s.UptimeSec = elapsed
				}
			}
		}
		out = append(out, s)
	}
	return out
}

func systemctlShow(unit string) map[string]string {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "systemctl", "show", unit,
		"--property=Description,ActiveState,SubState,MainPID,MemoryCurrent,CPUUsageNSec,ActiveEnterTimestampMonotonic",
	).Output()
	if err != nil {
		return map[string]string{}
	}
	m := map[string]string{}
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		line := sc.Text()
		idx := strings.Index(line, "=")
		if idx < 0 {
			continue
		}
		m[line[:idx]] = line[idx+1:]
	}
	return m
}
