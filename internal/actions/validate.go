package actions

import (
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/g4f4r0/tender/internal/server"
)

// unitPattern is a systemd unit name, such as tenderd, tenderd.service or getty@tty1.service. It
// can't start with "-", so it can never read as a flag, and has no glob characters.
var unitPattern = regexp.MustCompile(`^[A-Za-z0-9:_.@\\][A-Za-z0-9:_.@\\-]{0,254}$`)

// ParseSince reads a duration such as 30m, 1h, 1h30m or 7d: Go's units, and d for days. It must
// be more than zero and at most MaxSince.
func ParseSince(s string) (time.Duration, error) {
	bad := server.Invalid("since must be a duration such as 30m, 1h or 7d, at most 7d.")
	var d time.Duration
	if days, ok := strings.CutSuffix(s, "d"); ok {
		n, err := strconv.Atoi(days)
		if err != nil || n < 1 || n > 7 {
			return 0, bad
		}
		d = time.Duration(n) * 24 * time.Hour
	} else {
		var err error
		if d, err = time.ParseDuration(s); err != nil {
			return 0, bad
		}
	}
	if d <= 0 || d > MaxSince {
		return 0, bad
	}
	return d, nil
}

// validate checks the input for its type and fills in the default lines of a logs action.
func (in *Input) validate() error {
	if in.Packages != nil && in.Type != TypeUpgrade {
		return server.Invalid("packages is only for an upgrade action.")
	}
	switch in.Type {
	case TypeStart, TypeStop, TypeRestart:
		switch {
		case in.Service == nil || *in.Service == "":
			return server.Invalid("service is required for %s.", in.Type)
		case in.Unit != nil, in.Priority != nil, in.Since != nil, in.Lines != nil:
			return server.Invalid("unit, priority, since and lines are only for a logs action.")
		}
		return nil
	case TypeUpgrade:
		switch {
		case in.Service != nil, in.Unit != nil, in.Priority != nil, in.Since != nil, in.Lines != nil:
			return server.Invalid("An upgrade takes only packages: security or all.")
		case in.Packages == nil:
			p := PackagesSecurity
			in.Packages = &p
		case *in.Packages != PackagesSecurity && *in.Packages != PackagesAll:
			return server.Invalid("packages must be security (security updates only, the default) or all (every pending update).")
		}
		return nil
	case TypeUpdate:
		return server.Invalid("An update of Tender goes through a rollout, which updates one machine at a time: POST /v1/updates/rollouts with {\"machines\": [\"NAME\"]}.")
	case TypeLogs:
	default:
		return server.Invalid("type must be start, stop, restart, logs or upgrade.")
	}
	if in.Service != nil {
		if *in.Service == "" {
			return server.Invalid("service can't be empty. Leave it out to read the machine's logs.")
		}
		if in.Unit != nil || in.Priority != nil {
			return server.Invalid("unit and priority are only for the machine's logs; leave out service to read them.")
		}
	}
	if in.Unit != nil && !unitPattern.MatchString(*in.Unit) {
		return server.Invalid("unit must be a systemd unit name such as tenderd or nginx.service.")
	}
	if in.Priority != nil && !slices.Contains(Priorities, *in.Priority) {
		return server.Invalid("priority must be one of %s.", strings.Join(Priorities, ", "))
	}
	if in.Since != nil {
		if _, err := ParseSince(*in.Since); err != nil {
			return err
		}
	}
	if in.Lines == nil {
		n := DefaultLines
		in.Lines = &n
	}
	if *in.Lines < 1 || *in.Lines > MaxLines {
		return server.Invalid("lines must be a whole number from 1 to %d.", MaxLines)
	}
	return nil
}
