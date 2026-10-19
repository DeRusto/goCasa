// Package config reads GoCasa's env-only configuration (spec §11).
package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// DefaultAddr is ADDR when unset.
const DefaultAddr = ":8080"

type Config struct {
	Addr          string
	BaseURL       *url.URL
	DataDir       string
	Location      *time.Location // from TZ: the Household's default zone
	EncryptionKey []byte         // 32 bytes, AES-256
	SessionSecret []byte
	// The Household's own Google OAuth client; empty ID = Google not set up.
	GoogleClientID, GoogleClientSecret string
	GoogleRedirectURI                  string // default ${BASE_URL}/auth/google/callback
}

// Load builds a Config from getenv (os.Getenv in main, a map in tests).
func Load(getenv func(string) string) (Config, error) {
	c := Config{
		Addr:    getenv("ADDR"),
		DataDir: getenv("DATA_DIR"),

		GoogleClientID:     getenv("GOOGLE_CLIENT_ID"),
		GoogleClientSecret: getenv("GOOGLE_CLIENT_SECRET"),
		GoogleRedirectURI:  getenv("GOOGLE_REDIRECT_URI"),
	}
	if c.Addr == "" {
		c.Addr = DefaultAddr
	}
	if c.DataDir == "" {
		c.DataDir = "/data"
	}
	var errs []error

	if raw := getenv("BASE_URL"); raw == "" {
		errs = append(errs, errors.New("BASE_URL is required, e.g. https://casa.example.com"))
	} else if u, err := url.Parse(strings.TrimSuffix(raw, "/")); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.RawQuery != "" || u.Fragment != "" {
		errs = append(errs, fmt.Errorf("BASE_URL %q must be an absolute http(s) URL with no query or fragment", raw))
	} else {
		c.BaseURL = u
		if c.GoogleRedirectURI == "" {
			c.GoogleRedirectURI = u.String() + "/auth/google/callback"
		}
	}
	if (c.GoogleClientID == "") != (c.GoogleClientSecret == "") {
		errs = append(errs, errors.New("set both GOOGLE_CLIENT_ID and GOOGLE_CLIENT_SECRET, or neither"))
	}

	tz := getenv("TZ")
	if tz == "" {
		tz = "UTC"
	}
	if loc, err := time.LoadLocation(tz); err != nil {
		errs = append(errs, fmt.Errorf("TZ %q is not an IANA zone: %w", tz, err))
	} else {
		c.Location = loc
	}

	if key, err := base64.StdEncoding.DecodeString(getenv("APP_ENCRYPTION_KEY")); err != nil || len(key) != 32 {
		errs = append(errs, errors.New("APP_ENCRYPTION_KEY must be 32 random bytes, base64-encoded (openssl rand -base64 32)"))
	} else {
		c.EncryptionKey = key
	}

	if s := getenv("SESSION_SECRET"); len(s) < 32 {
		errs = append(errs, errors.New("SESSION_SECRET must be at least 32 characters (openssl rand -base64 32)"))
	} else {
		c.SessionSecret = []byte(s)
	}

	return c, errors.Join(errs...)
}
