// Package metrics writes the Prometheus text exposition format, version 0.0.4. It knows no
// domain: cmd/tender reads each domain's state and hands the numbers here.
package metrics

import (
	"fmt"
	"io"
	"math"
	"regexp"
	"strconv"
	"strings"
)

// ContentType is the Content-Type of the text format.
const ContentType = "text/plain; version=0.0.4; charset=utf-8"

// The metric types this package writes.
const (
	Gauge   = "gauge"
	Counter = "counter"
)

var (
	metricName = regexp.MustCompile(`^[a-zA-Z_:][a-zA-Z0-9_:]*$`)
	labelName  = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)
)

// Family is one metric: its name, help and type, and one sample per label set.
type Family struct {
	Name    string
	Help    string
	Type    string
	Samples []Sample
}

// Sample is one value of a family, with its labels as name, value pairs.
type Sample struct {
	Labels []string
	Value  float64
}

// New returns an empty family. A family without samples is still written, so a scraper sees
// that the metric exists and has no series now.
func New(name, typ, help string) *Family {
	return &Family{Name: name, Type: typ, Help: help}
}

// Add adds a sample. labels are name, value pairs, such as "machine", "apps".
func (f *Family) Add(v float64, labels ...string) {
	f.Samples = append(f.Samples, Sample{Labels: labels, Value: v})
}

// Write writes the families in order. It refuses a bad metric or label name, a type other than
// gauge or counter, an odd label list, a negative counter and a family named twice, before writing
// anything, so a scraper never gets half a page.
func Write(w io.Writer, fams []*Family) error {
	seen := map[string]bool{}
	for _, f := range fams {
		if err := f.check(); err != nil {
			return err
		}
		if seen[f.Name] {
			return fmt.Errorf("metrics: %s appears twice", f.Name)
		}
		seen[f.Name] = true
	}
	var b strings.Builder
	for _, f := range fams {
		_, _ = fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s %s\n", f.Name, helpEscaper.Replace(f.Help), f.Name, f.Type)
		for _, s := range f.Samples {
			b.WriteString(f.Name)
			if len(s.Labels) > 0 {
				b.WriteByte('{')
				for i := 0; i < len(s.Labels); i += 2 {
					if i > 0 {
						b.WriteByte(',')
					}
					_, _ = fmt.Fprintf(&b, `%s="%s"`, s.Labels[i], valueEscaper.Replace(s.Labels[i+1]))
				}
				b.WriteByte('}')
			}
			b.WriteByte(' ')
			b.WriteString(value(s.Value))
			b.WriteByte('\n')
		}
	}
	_, err := io.WriteString(w, b.String())
	return err
}

func (f *Family) check() error {
	if !metricName.MatchString(f.Name) {
		return fmt.Errorf("metrics: %q is not a metric name", f.Name)
	}
	if f.Type != Gauge && f.Type != Counter {
		return fmt.Errorf("metrics: %s has the type %q; it must be gauge or counter", f.Name, f.Type)
	}
	for _, s := range f.Samples {
		if len(s.Labels)%2 != 0 {
			return fmt.Errorf("metrics: a sample of %s has a label without a value", f.Name)
		}
		names := map[string]bool{}
		for i := 0; i < len(s.Labels); i += 2 {
			n := s.Labels[i]
			if !labelName.MatchString(n) || strings.HasPrefix(n, "__") || names[n] {
				return fmt.Errorf("metrics: a sample of %s has the bad or repeated label name %q", f.Name, n)
			}
			names[n] = true
		}
		if f.Type == Counter && (s.Value < 0 || math.IsNaN(s.Value)) {
			return fmt.Errorf("metrics: the counter %s has the value %v", f.Name, s.Value)
		}
	}
	return nil
}

var (
	helpEscaper  = strings.NewReplacer(`\`, `\\`, "\n", `\n`)
	valueEscaper = strings.NewReplacer(`\`, `\\`, "\n", `\n`, `"`, `\"`)
)

func value(v float64) string {
	switch {
	case math.IsNaN(v):
		return "NaN"
	case math.IsInf(v, 1):
		return "+Inf"
	case math.IsInf(v, -1):
		return "-Inf"
	case v == math.Trunc(v) && math.Abs(v) < 1e15: // whole numbers, such as bytes and timestamps, in full
		return strconv.FormatFloat(v, 'f', -1, 64)
	}
	return strconv.FormatFloat(v, 'g', -1, 64)
}
