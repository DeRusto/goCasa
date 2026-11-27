package mdns

import (
	"bytes"
	"context"
	"log/slog"
	"maps"
	"strings"
	"testing"
	"time"
)

func TestServiceDescribesGoCasaForTheDisplayApp(t *testing.T) {
	s := Service("Casa Kitchen", 8080, "v1.2.0")
	if s.Name != "Casa Kitchen" || s.Type != "_gocasa._tcp" || s.Port != 8080 {
		t.Fatalf("name %q type %q port %d", s.Name, s.Type, s.Port)
	}
	if want := map[string]string{"txtvers": "1", "ver": "v1.2.0"}; !maps.Equal(s.Text, want) {
		t.Fatalf("TXT = %v, want %v", s.Text, want)
	}
}

func TestAdvertiseStopsWhenTheServerStops(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	done := Advertise(ctx, slog.New(slog.DiscardHandler), Service("GoCasa test", 1, "test"))
	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("advertising still running 5s after shutdown")
	}
}

func TestAdvertiseLogsAFailureInsteadOfCrashing(t *testing.T) {
	var logs bytes.Buffer
	done := Advertise(context.Background(), slog.New(slog.NewTextHandler(&logs, nil)), Service("", 8080, "test"))
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("a service with no name kept advertising")
	}
	if !strings.Contains(logs.String(), "mdns advertising stopped") {
		t.Fatalf("logs = %q, want the failure logged", logs.String())
	}
}
