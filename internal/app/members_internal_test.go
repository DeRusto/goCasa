package app

import "testing"

func TestSafeNext(t *testing.T) {
	t.Parallel()
	for next, want := range map[string]string{
		"/calendar?view=week":  "/calendar?view=week",
		"/todos":               "/todos",
		"":                     "",
		"calendar":             "",
		"//evil.example":       "",
		`/\evil.example`:       "",
		`/a\b`:                 "",
		"/a\tb":                "",
		"/a\nb":                "",
		"/a\x7fb":              "",
		"https://evil.example": "",
	} {
		if got := safeNext(next); got != want {
			t.Errorf("safeNext(%q) = %q, want %q", next, got, want)
		}
	}
}
