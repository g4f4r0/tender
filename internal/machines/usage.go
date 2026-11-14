package machines

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// ReadUsage measures this machine: 1-minute load, memory used %, disk used % of "/" and uptime.
// proc is the /proc directory, a parameter so tests can point it at fixture files.
func ReadUsage(proc string) (UsageInput, error) {
	u := UsageInput{CreatedAt: time.Now().UTC().Truncate(time.Second)}

	load, err := firstField(filepath.Join(proc, "loadavg"))
	if err != nil {
		return u, err
	}
	if u.CPULoad, err = strconv.ParseFloat(load, 64); err != nil {
		return u, fmt.Errorf("loadavg: %w", err)
	}

	up, err := firstField(filepath.Join(proc, "uptime"))
	if err != nil {
		return u, err
	}
	seconds, err := strconv.ParseFloat(up, 64)
	if err != nil {
		return u, fmt.Errorf("uptime: %w", err)
	}
	u.UptimeSeconds = int64(seconds)

	if u.MemoryUsedPercent, err = memoryUsedPercent(filepath.Join(proc, "meminfo")); err != nil {
		return u, err
	}
	if u.DiskUsedPercent, err = diskUsedPercent("/"); err != nil {
		return u, err
	}
	return u, nil
}

func firstField(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	f := strings.Fields(string(b))
	if len(f) == 0 {
		return "", fmt.Errorf("%s is empty", path)
	}
	return f[0], nil
}

// memoryUsedPercent is (MemTotal - MemAvailable) / MemTotal from /proc/meminfo.
func memoryUsedPercent(path string) (float64, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	var total, available float64
	var haveTotal, haveAvailable bool
	for _, line := range strings.Split(string(b), "\n") {
		name, rest, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		f := strings.Fields(rest)
		if len(f) == 0 {
			continue
		}
		kb, err := strconv.ParseFloat(f[0], 64)
		if err != nil {
			return 0, fmt.Errorf("meminfo %s: %w", name, err)
		}
		switch name {
		case "MemTotal":
			total, haveTotal = kb, true
		case "MemAvailable":
			available, haveAvailable = kb, true
		}
	}
	if !haveTotal || !haveAvailable || total == 0 {
		return 0, fmt.Errorf("%s has no MemTotal and MemAvailable", path)
	}
	return round2((total - available) / total * 100), nil
}

// diskUsedPercent follows df: used / (used + space available to unprivileged users).
func diskUsedPercent(path string) (float64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, fmt.Errorf("statfs %s: %w", path, err)
	}
	used := float64(st.Blocks - st.Bfree)
	avail := float64(st.Bavail)
	if used+avail == 0 {
		return 0, nil
	}
	return round2(used / (used + avail) * 100), nil
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }
