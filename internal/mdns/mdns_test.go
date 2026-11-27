package mdns

import (
	"bytes"
	"context"
	"log/slog"
	"maps"
	"net"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestServiceDescribesGoCasaForTheDisplayApp(t *testing.T) {
	s, err := Service("Casa Kitchen", &net.TCPAddr{Port: 8080}, "v1.2.0")
	if err != nil {
		t.Fatal(err)
	}
	if s.Ifaces != nil {
		t.Fatalf("listening on every address, but advertised only on %v", s.Ifaces)
	}
	if s.Name != "Casa Kitchen" || s.Type != "_gocasa._tcp" || s.Port != 8080 {
		t.Fatalf("name %q type %q port %d", s.Name, s.Type, s.Port)
	}
	if want := map[string]string{"txtvers": "1", "ver": "v1.2.0"}; !maps.Equal(s.Text, want) {
		t.Fatalf("TXT = %v, want %v", s.Text, want)
	}
}

func TestServiceRefusesALoopbackListener(t *testing.T) {
	_, err := Service("GoCasa", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 8080}, "v1.2.0")
	if err == nil || !strings.Contains(err.Error(), "loopback") {
		t.Fatalf("err = %v, want a loopback refusal", err)
	}
}

func TestServiceOnOneAddressAdvertisesOnlyItsInterface(t *testing.T) {
	ifaces, _ := net.Interfaces()
	for _, ifi := range ifaces {
		addrs, _ := ifi.Addrs()
		for _, a := range addrs {
			ip, ok := a.(*net.IPNet)
			if !ok || ip.IP.IsLoopback() || ip.IP.To4() == nil {
				continue
			}
			s, err := Service("GoCasa", &net.TCPAddr{IP: ip.IP, Port: 8080}, "v1.2.0")
			if err != nil || !slices.Equal(s.Ifaces, []string{ifi.Name}) {
				t.Fatalf("listening on %v: Ifaces = %v, err = %v; want [%s]", ip.IP, s.Ifaces, err, ifi.Name)
			}
			if len(s.IPs) != 1 || !s.IPs[0].Equal(ip.IP) {
				t.Fatalf("listening on %v, but advertised %v", ip.IP, s.IPs)
			}
			return
		}
	}
	t.Skip("no non-loopback IPv4 address on this machine")
}

func TestAdvertiseStopsWhenTheServerStops(t *testing.T) {
	svc, _ := Service("GoCasa test", &net.TCPAddr{Port: 1}, "test")
	var logs bytes.Buffer
	ctx, cancel := context.WithCancel(context.Background())
	done := Advertise(ctx, slog.New(slog.NewTextHandler(&logs, nil)), svc)
	time.Sleep(2 * time.Second) // past probing and the first announcement
	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("advertising still running 10s after shutdown")
	}
	if !strings.Contains(logs.String(), "mdns advertising starting") || strings.Contains(logs.String(), "stopped") {
		t.Fatalf("logs = %q, want a clean start and stop", logs.String())
	}
}

func TestAdvertiseLogsAFailureInsteadOfCrashing(t *testing.T) {
	var logs bytes.Buffer
	svc, _ := Service("", &net.TCPAddr{Port: 8080}, "test")
	done := Advertise(context.Background(), slog.New(slog.NewTextHandler(&logs, nil)), svc)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("a service with no name kept advertising")
	}
	if !strings.Contains(logs.String(), "mdns advertising stopped") {
		t.Fatalf("logs = %q, want the failure logged", logs.String())
	}
}
