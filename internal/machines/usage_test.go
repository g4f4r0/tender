package machines

import (
	"os"
	"path/filepath"
	"testing"
)

func writeProc(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestReadUsage(t *testing.T) {
	proc := writeProc(t, map[string]string{
		"loadavg": "0.52 0.40 0.31 1/812 12345\n",
		"uptime":  "86400.75 170000.10\n",
		"meminfo": "MemTotal:       16000000 kB\nMemFree:         1000000 kB\nMemAvailable:    4000000 kB\nBuffers: 10 kB\n",
	})
	u, err := ReadUsage(proc)
	if err != nil {
		t.Fatal(err)
	}
	if u.CPULoad != 0.52 {
		t.Errorf("cpu_load = %v, want 0.52", u.CPULoad)
	}
	if u.UptimeSeconds != 86400 {
		t.Errorf("uptime_seconds = %d, want 86400", u.UptimeSeconds)
	}
	if u.MemoryUsedPercent != 75 {
		t.Errorf("memory_used_percent = %v, want 75", u.MemoryUsedPercent)
	}
	if u.DiskUsedPercent <= 0 || u.DiskUsedPercent > 100 {
		t.Errorf("disk_used_percent = %v, want between 0 and 100", u.DiskUsedPercent)
	}
	if u.CreatedAt.IsZero() || u.CreatedAt.Location().String() != "UTC" || u.CreatedAt.Nanosecond() != 0 {
		t.Errorf("created_at = %v, want UTC whole seconds", u.CreatedAt)
	}
}

func TestReadUsageBadInput(t *testing.T) {
	good := map[string]string{
		"loadavg": "0.1 0.1 0.1 1/1 1\n",
		"uptime":  "10.0 10.0\n",
		"meminfo": "MemTotal: 100 kB\nMemAvailable: 50 kB\n",
	}
	for name, bad := range map[string]string{
		"loadavg": "abc\n",
		"uptime":  "\n",
		"meminfo": "MemTotal: 100 kB\n",
	} {
		files := map[string]string{}
		for k, v := range good {
			files[k] = v
		}
		files[name] = bad
		if _, err := ReadUsage(writeProc(t, files)); err == nil {
			t.Errorf("bad %s: want an error", name)
		}
	}
	if _, err := ReadUsage(t.TempDir()); err == nil {
		t.Error("missing files: want an error")
	}
}
