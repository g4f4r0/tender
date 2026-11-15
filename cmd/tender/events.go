package main

import (
	"slices"

	"github.com/g4f4r0/tender/internal/notifications"
)

// domainEvents are the events the other domains' problems fire, one list per domain, each in
// its own file here, such as logEvents in logs_events.go. The wiring finds those problems and hands
// them to the rules as notifications.Finding, so no domain imports notifications. They are
// registered before the hub starts; the notifications domain registers its rules' events itself.
var domainEvents = slices.Concat(logEvents, securityEvents, updateEvents)

func init() { notifications.Register(domainEvents...) }
