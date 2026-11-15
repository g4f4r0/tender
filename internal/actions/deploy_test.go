package actions

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// A service's container narrows an action, and the services domain hears which one finished.
func TestContainerActions(t *testing.T) {
	svc := &fakeServices{}
	s := NewStore(newDB(t), svc)
	ctx := context.Background()
	if _, err := s.Create(ctx, 1, Input{Type: TypeRestart, Service: ptr("web"), Container: ptr("nope")}); status(err) != http.StatusNotFound {
		t.Errorf("unknown container: %v", err)
	}
	if _, err := s.Create(ctx, 1, Input{Type: TypeRestart, Container: ptr("app")}); status(err) != http.StatusUnprocessableEntity {
		t.Errorf("container without service: %v", err)
	}
	if _, err := s.Create(ctx, 1, Input{Type: TypeRestart, Service: ptr("web"), Container: ptr("")}); status(err) != http.StatusUnprocessableEntity {
		t.Errorf("empty container: %v", err)
	}
	a, err := s.Create(ctx, 1, Input{Type: TypeStop, Service: ptr("web"), Container: ptr("app")})
	if err != nil || deref(a.Container) != "app" {
		t.Fatalf("stop = %+v, %v", a, err)
	}
	if _, err := s.Take(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if err := s.Claim(ctx, 1, a.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Finish(ctx, 1, a.ID, Patch{Status: StatusCompleted}); err != nil {
		t.Fatal(err)
	}
	if len(svc.finished) != 1 || svc.finished[0] != "stop web/app" {
		t.Errorf("finished = %v", svc.finished)
	}
}

// Only the services domain queues deploys and removes, and the daemon gets the service with them.
func TestDeployActions(t *testing.T) {
	s := NewStore(newDB(t), &fakeServices{})
	ctx := context.Background()
	for _, typ := range []string{TypeDeploy, TypeRemove} {
		if _, err := s.Create(ctx, 1, Input{Type: typ, Service: ptr("web")}); status(err) != http.StatusUnprocessableEntity {
			t.Errorf("%s through the actions collection: %v", typ, err)
		}
	}
	dep, err := s.CreateDeploy(ctx, 1, TypeDeploy, "notes", true)
	if err != nil || dep.DeleteData != nil || deref(dep.Service) != "notes" {
		t.Fatalf("deploy = %+v, %v", dep, err)
	}
	list, err := s.Take(ctx, 1)
	if err != nil || len(list) != 1 || string(list[0].Deployment) != `{"name":"notes"}` {
		t.Fatalf("take = %+v, %v", list, err)
	}
	if err := s.Claim(ctx, 1, dep.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Finish(ctx, 1, dep.ID, Patch{Status: StatusCompleted}); err != nil {
		t.Fatal(err)
	}
	rm, err := s.CreateDeploy(ctx, 1, TypeRemove, "notes", true)
	if err != nil || rm.DeleteData == nil || !*rm.DeleteData {
		t.Fatalf("remove = %+v, %v", rm, err)
	}
	b, _ := json.Marshal(rm.Summary)
	if !json.Valid(b) || !strings.Contains(string(b), `"delete_data":true`) {
		t.Errorf("remove JSON = %s", b)
	}
	if err := s.Finish(ctx, 1, rm.ID, Patch{Status: StatusFailed}); err == nil {
		t.Error("a pending remove finished")
	}

	// A service the hub can't make ready fails at once, with why, and the daemon gets nothing.
	broken, err := s.CreateDeploy(ctx, 2, TypeDeploy, "broken", false)
	if err != nil {
		t.Fatal(err)
	}
	if list, err := s.Take(ctx, 2); err != nil || len(list) != 0 {
		t.Fatalf("take of a broken deploy = %+v, %v", list, err)
	}
	got, err := s.Get(ctx, 2, broken.ID)
	if err != nil || got.Status != StatusFailed || deref(got.Output) != "Cloudflare refused the tunnel's routes." {
		t.Errorf("broken deploy = %+v, %v", got, err)
	}
}
