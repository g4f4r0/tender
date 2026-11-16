package services

import (
	"sort"
	"strconv"
	"strings"
)

// The compose labels that name a container in its service.
const (
	composeProject = "com.docker.compose.project"
	composeService = "com.docker.compose.service"
	composeNumber  = "com.docker.compose.container-number"
)

// Named is where a container belongs: its service and its name in the service.
type Named struct {
	Service, Container string
}

// Name files each container of a report under its service and gives it its name there. The service
// is its compose project, or the container itself without one. In a project, a container is named
// by its compose service, such as web, with -2 and up for later replicas; without the labels that
// say so, by its container name less the project's prefix and a -1 suffix (myapp-web-1 is web).
// Two containers of a service that would get one name are told apart by the start of their
// container id, in the order of their container names. The hub and the daemon name the same list
// the same way, so an action can say which container it means.
func Name(list []ContainerInput) []Named {
	order := make([]int, len(list))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool { return list[order[a]].Name < list[order[b]].Name })
	out := make([]Named, len(list))
	taken := map[Named]bool{}
	for _, i := range order {
		c := list[i]
		n := Named{Service: c.Name, Container: c.Name}
		if c.Project != nil && *c.Project != "" {
			n = Named{Service: *c.Project, Container: inProject(c)}
		}
		if taken[n] {
			n.Container += "-" + shortID(c.ContainerID)
		}
		taken[n] = true
		out[i] = n
	}
	return out
}

// inProject is the name of a container in its compose project.
func inProject(c ContainerInput) string {
	if c.ComposeService != nil && *c.ComposeService != "" {
		if c.ContainerNumber != nil && *c.ContainerNumber > 1 {
			return *c.ComposeService + "-" + strconv.Itoa(*c.ContainerNumber)
		}
		return *c.ComposeService
	}
	rest, ok := strings.CutPrefix(c.Name, *c.Project+"-")
	if !ok || rest == "" {
		return c.Name
	}
	if short, ok := strings.CutSuffix(rest, "-1"); ok && short != "" {
		return short
	}
	return rest
}

func shortID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}
