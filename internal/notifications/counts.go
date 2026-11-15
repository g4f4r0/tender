package notifications

import (
	"context"
	"slices"
)

// OpenCount is how many alerts of one type and severity are open: pending or sent, not resolved.
type OpenCount struct {
	Type     string
	Severity string
	Count    int
}

// OpenAlerts counts the open alerts of every type at every severity, zeros included, so a missing
// series never stands for none. It is sorted by type, then severity from info to critical.
func (s *Store) OpenAlerts(ctx context.Context) ([]OpenCount, error) {
	rows, err := s.db.Read.QueryContext(ctx,
		`SELECT type, severity, count(*) FROM notifications WHERE status != ? GROUP BY type, severity`, StatusResolved)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	got := map[[2]string]int{}
	for rows.Next() {
		var typ, sev string
		var n int
		if err := rows.Scan(&typ, &sev, &n); err != nil {
			return nil, err
		}
		got[[2]string{typ, sev}] = n
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	types := make([]string, 0, len(typeText))
	for t := range typeText {
		types = append(types, t)
	}
	for k := range got { // a type a newer hub wrote, after a downgrade
		if _, ok := typeText[k[0]]; !ok && !slices.Contains(types, k[0]) {
			types = append(types, k[0])
		}
	}
	slices.Sort(types)
	out := make([]OpenCount, 0, len(types)*3)
	for _, t := range types {
		for _, sev := range []string{SeverityInfo, SeverityWarning, SeverityCritical} {
			out = append(out, OpenCount{Type: t, Severity: sev, Count: got[[2]string{t, sev}]})
		}
	}
	return out, nil
}
