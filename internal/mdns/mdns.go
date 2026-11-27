// Package mdns advertises GoCasa on the LAN over DNS-SD, so the Display app
// can find it without anyone typing an address.
package mdns

import (
	"context"
	"errors"
	"log/slog"
	"sync"

	"github.com/brutella/dnssd"
	dnssdlog "github.com/brutella/dnssd/log"
)

// Type is GoCasa's DNS-SD service type.
const Type = "_gocasa._tcp"

// Service describes the advert: name is the instance name (the library renames
// it on a LAN conflict, e.g. "GoCasa (2)"), port the one GoCasa listens on.
// The Display app opens http://<resolved address>:<port>/ and reads ver only to
// say "GoCasa too old".
func Service(name string, port int, version string) dnssd.Config {
	return dnssd.Config{
		Name: name,
		Type: Type,
		Port: port,
		Text: map[string]string{"txtvers": "1", "ver": version},
	}
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
	log.Info("mdns advertising", "name", cfg.Name, "type", cfg.Type, "port", cfg.Port)
	return rp.Respond(ctx)
}
