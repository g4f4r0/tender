package notifications

import (
	"context"
	"slices"

	"github.com/g4f4r0/tender/internal/notifications/channels"
)

// OpenCount is how many problems of one event are open at one severity.
type OpenCount struct {
	Event    string // the base event, such as service.down
	Severity string // the severity of each problem's latest notification
	Count    int
}

// OpenProblems counts the open problems of every registered event at every severity, zeros
// included, so a missing series never stands for none. It is sorted by event, then severity
// from info to critical. Tests are not problems.
func (s *Store) OpenProblems(ctx context.Context) ([]OpenCount, error) {
	rows, err := s.db.Read.QueryContext(ctx, `SELECT n.event, n.severity, count(*) FROM notifications n WHERE `+latestOpen+` GROUP BY n.event, n.severity`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	got := map[[2]string]int{}
	for rows.Next() {
		var event, sev string
		var n int
		if err := rows.Scan(&event, &sev, &n); err != nil {
			return nil, err
		}
		got[[2]string{channels.Base(event), sev}] += n
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var events []string
	for _, t := range Events() {
		if t.Name != EventTest {
			events = append(events, t.Name)
		}
	}
	for k := range got { // an event a newer hub wrote, after a downgrade
		if !slices.Contains(events, k[0]) {
			events = append(events, k[0])
		}
	}
	slices.Sort(events)
	out := make([]OpenCount, 0, len(events)*3)
	for _, e := range events {
		for _, sev := range []string{SeverityInfo, SeverityWarning, SeverityCritical} {
			out = append(out, OpenCount{Event: e, Severity: sev, Count: got[[2]string{e, sev}]})
		}
	}
	return out, nil
}
