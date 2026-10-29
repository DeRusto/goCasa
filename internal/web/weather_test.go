package web

import (
	"strings"
	"testing"
)

func TestWeatherIconFiles(t *testing.T) {
	for c := range wxIconFile {
		for _, day := range []bool{true, false} {
			if svg := weatherIcon(c, day, "size-8"); !strings.HasPrefix(svg, `<svg viewBox="12 12 104 104" class="size-8"`) {
				t.Errorf("%s (day %v): %.80s", c, day, svg)
			}
		}
	}
	if !strings.Contains(weatherIcon("", true, ""), `id="not-available"`) {
		t.Error("an unknown condition should show not-available")
	}
}
