package config

import (
	"strings"
	"testing"
)

const key32 = "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=" // base64 of 32 ASCII bytes

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func valid() map[string]string {
	return map[string]string{
		"BASE_URL":           "https://casa.example.com/",
		"TZ":                 "America/Los_Angeles",
		"APP_ENCRYPTION_KEY": key32,
		"SESSION_SECRET":     strings.Repeat("s", 32),
	}
}

func TestLoadDefaultsAndParses(t *testing.T) {
	c, err := Load(env(valid()))
	if err != nil {
		t.Fatal(err)
	}
	if c.Addr != ":8080" || c.DataDir != "/data" {
		t.Fatalf("defaults: addr %q data %q", c.Addr, c.DataDir)
	}
	if c.BaseURL.String() != "https://casa.example.com" {
		t.Fatalf("BaseURL = %q, want trailing slash trimmed", c.BaseURL)
	}
	if c.Location.String() != "America/Los_Angeles" || len(c.EncryptionKey) != 32 {
		t.Fatal("TZ or key not parsed")
	}
}

func TestLoadRejectsBadValues(t *testing.T) {
	for name, tc := range map[string]struct{ key, val, want string }{
		"missing base url":  {"BASE_URL", "", "BASE_URL"},
		"relative base url": {"BASE_URL", "casa.example.com", "BASE_URL"},
		"bad tz":            {"TZ", "Mars/Olympus", "TZ"},
		"short key":         {"APP_ENCRYPTION_KEY", "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZQ==", "APP_ENCRYPTION_KEY"}, // 31 bytes
		"key not base64":    {"APP_ENCRYPTION_KEY", "not base64!", "APP_ENCRYPTION_KEY"},
		"short secret":      {"SESSION_SECRET", strings.Repeat("s", 31), "SESSION_SECRET"},
	} {
		t.Run(name, func(t *testing.T) {
			e := valid()
			e[tc.key] = tc.val
			if _, err := Load(env(e)); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want mention of %s", err, tc.want)
			}
		})
	}
}
