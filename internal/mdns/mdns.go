// Package mdns advertises GoCasa on the LAN over DNS-SD, so the Display app
// can find it without anyone typing an address.
package mdns

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"

	"github.com/brutella/dnssd"
	dnssdlog "github.com/brutella/dnssd/log"
)

// Type is GoCasa's DNS-SD service type.
const Type = "_gocasa._tcp"

// Service describes the advert for GoCasa listening at ln: name is the
// instance name (the library renames it on a LAN conflict, e.g. "GoCasa (2)").
// The Display app opens http://<resolved address>:<port>/ and reads ver only to
// say "GoCasa too old". A listener on one address is advertised only on that
// address's interface; a loopback one can't be reached from the LAN, so it is
// refused.
func Service(name string, ln *net.TCPAddr, version string) (dnssd.Config, error) {
	cfg := dnssd.Config{
		Name: name,
		Type: Type,
		Port: ln.Port,
		Text: map[string]string{"txtvers": "1", "ver": version},
	}
	switch {
	case ln.IP == nil || ln.IP.IsUnspecified():
		return cfg, nil
	case ln.IP.IsLoopback():
		return cfg, fmt.Errorf("GoCasa listens on loopback %s, which the LAN can't reach; set ADDR to :%d or a LAN address", ln.IP, ln.Port)
	}
	iface, err := interfaceOf(ln.IP)
	if err != nil {
		return cfg, err
	}
	cfg.Ifaces = []string{iface}
	return cfg, nil
}

func interfaceOf(ip net.IP) (string, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return "", err
	}
	for _, ifi := range ifaces {
		addrs, _ := ifi.Addrs()
		for _, a := range addrs {
			if n, ok := a.(*net.IPNet); ok && n.IP.Equal(ip) {
				return ifi.Name, nil
			}
		}
	}
	return "", fmt.Errorf("no network interface has GoCasa's listen address %s", ip)
}

// Advertise announces svc until ctx ends, then sends the goodbye. It never
// stops the server: any failure is logged. The returned channel closes once
// advertising has stopped.
func Advertise(ctx context.Context, log *slog.Logger, svc dnssd.Config) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := respond(ctx, log, svc); err != nil && !errors.Is(err, context.Canceled) {
			log.Error("mdns advertising stopped", "err", err)
		}
	}()
	return done
}

// quietLibrary sends the library's plain-text notes (link updates, other
// devices' malformed packets) into GoCasa's log at debug level.
var quietLibrary sync.Once

func respond(ctx context.Context, log *slog.Logger, cfg dnssd.Config) error {
	quietLibrary.Do(func() { dnssdlog.Info.Logger = slog.NewLogLogger(log.Handler(), slog.LevelDebug) })
	svc, err := dnssd.NewService(cfg)
	if err != nil {
		return err
	}
	rp, err := dnssd.NewResponder()
	if err != nil {
		return err
	}
	if _, err := rp.Add(svc); err != nil {
		return err
	}
	log.Info("mdns advertising starting", "name", cfg.Name, "type", cfg.Type, "port", cfg.Port)
	return rp.Respond(ctx)
}
