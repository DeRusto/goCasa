package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadDotEnv(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	body := "\xef\xbb\xbf# comment\r\n\r\nGOCASA_T_NEW = plain\r\nGOCASA_T_QUOTED=\"a=b c\"\nGOCASA_T_SET=from-file\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOCASA_T_SET", "from-env")
	for _, k := range []string{"GOCASA_T_NEW", "GOCASA_T_QUOTED"} {
		t.Setenv(k, "") // restored after the test
		os.Unsetenv(k)
	}
	if err := LoadDotEnv(path); err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]string{"GOCASA_T_NEW": "plain", "GOCASA_T_QUOTED": "a=b c", "GOCASA_T_SET": "from-env"} {
		if got := os.Getenv(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}

	if err := LoadDotEnv(filepath.Join(t.TempDir(), "missing")); err != nil {
		t.Fatalf("missing file: %v", err)
	}
	if err := os.WriteFile(path, []byte("no equals sign\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := LoadDotEnv(path); err == nil {
		t.Fatal("malformed line accepted")
	}
}
