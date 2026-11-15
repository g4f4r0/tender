package metrics

import (
	"math"
	"strings"
	"testing"
)

func TestWrite(t *testing.T) {
	up := New("tender_machine_up", Gauge, "Whether the machine reports.\nOne line.")
	up.Add(1, "machine", "apps")
	up.Add(0, "machine", `we"ird\name`+"\n")
	big := New("tender_backup_last_size_bytes", Gauge, `A \ backslash`)
	big.Add(1.5e12, "machine", "apps")
	big.Add(math.NaN())
	big.Add(1e21, "huge", "1")
	big.Add(0.000015, "small", "1")
	big.Add(math.Inf(1), "x", "y")
	big.Add(math.Inf(-1), "x", "z")
	empty := New("tender_log_lines_shipped", Counter, "Nothing yet.")
	var out strings.Builder
	if err := Write(&out, []*Family{up, big, empty}); err != nil {
		t.Fatal(err)
	}
	want := `# HELP tender_machine_up Whether the machine reports.\nOne line.
# TYPE tender_machine_up gauge
tender_machine_up{machine="apps"} 1
tender_machine_up{machine="we\"ird\\name\n"} 0
# HELP tender_backup_last_size_bytes A \\ backslash
# TYPE tender_backup_last_size_bytes gauge
tender_backup_last_size_bytes{machine="apps"} 1500000000000
tender_backup_last_size_bytes NaN
tender_backup_last_size_bytes{huge="1"} 1e+21
tender_backup_last_size_bytes{small="1"} 1.5e-05
tender_backup_last_size_bytes{x="y"} +Inf
tender_backup_last_size_bytes{x="z"} -Inf
# HELP tender_log_lines_shipped Nothing yet.
# TYPE tender_log_lines_shipped counter
`
	if out.String() != want {
		t.Errorf("got:\n%s\nwant:\n%s", out.String(), want)
	}
}

func TestWriteRefuses(t *testing.T) {
	for name, f := range map[string]*Family{
		"metric name": {Name: "tender-up", Type: Gauge},
		"type":        {Name: "x", Type: "summary"},
		"odd labels":  {Name: "x", Type: Gauge, Samples: []Sample{{Labels: []string{"machine"}}}},
		"label name":  {Name: "x", Type: Gauge, Samples: []Sample{{Labels: []string{"ma-chine", "a"}}}},
		"reserved":    {Name: "x", Type: Gauge, Samples: []Sample{{Labels: []string{"__name__", "a"}}}},
		"repeated":    {Name: "x", Type: Gauge, Samples: []Sample{{Labels: []string{"a", "1", "a", "2"}}}},
		"negative":    {Name: "x", Type: Counter, Samples: []Sample{{Value: -1}}},
	} {
		var out strings.Builder
		if err := Write(&out, []*Family{New("ok", Gauge, "fine"), f}); err == nil || out.Len() != 0 {
			t.Errorf("%s: %v, wrote %q; want an error and nothing written", name, err, out.String())
		}
	}
	var out strings.Builder
	if err := Write(&out, []*Family{New("x", Gauge, ""), New("x", Gauge, "")}); err == nil || out.Len() != 0 {
		t.Errorf("twice: %v", err)
	}
}
