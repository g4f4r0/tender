package main

import "github.com/g4f4r0/tender/internal/notifications"

// domainEvents are the events the other domains fire. Each domain names its events in its own
// package as plain values, so it never imports notifications; this wiring turns them into
// notifications.EventType and registers them before the hub starts. The notifications domain
// registers its own, the rules' events, itself.
var domainEvents []notifications.EventType

func init() { notifications.Register(domainEvents...) }
