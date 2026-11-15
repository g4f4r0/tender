package services

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestPublishedPorts(t *testing.T) {
	dir, err := os.MkdirTemp("", "docker")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket := filepath.Join(dir, "docker.sock")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{ReadHeaderTimeout: time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RequestURI() != "/containers/json?all=1" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`[
			{"Names":["/web"],"State":"running","Ports":[{"IP":"0.0.0.0","PrivatePort":80,"PublicPort":8080,"Type":"tcp"},
				{"IP":"::","PrivatePort":80,"PublicPort":8080,"Type":"tcp"},{"PrivatePort":9000,"Type":"tcp"}]},
			{"Names":["/old"],"State":"exited","Ports":[{"IP":"0.0.0.0","PrivatePort":80,"PublicPort":8081,"Type":"tcp"}]},
			{"Names":["/dns"],"State":"running","Ports":[{"IP":"127.0.0.1","PrivatePort":53,"PublicPort":5353,"Type":"udp"}]}]`))
	})}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })

	got, err := NewDocker(socket).Published(context.Background())
	want := []Port{{"tcp", "0.0.0.0", 8080, "web"}, {"tcp", "::", 8080, "web"}, {"udp", "127.0.0.1", 5353, "dns"}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("published = %+v %v", got, err)
	}
}
