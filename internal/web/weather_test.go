package web

import (
	"strings"
	"testing"
)

func TestWeatherIconFiles(t *testing.T) {
	for c, f := range wxIconFile {
		if (f[0] != f[1]) == (weatherIcon(c, true, "") == weatherIcon(c, false, "")) {
			t.Errorf("%s: day and night icons should differ exactly when their files do", c)
		}
		for _, day := range []bool{true, false} {
			svg := weatherIcon(c, day, "size-8")
			if !strings.HasPrefix(svg, `<svg viewBox="12 12 104 104" class="size-8"`) || strings.Count(svg, `<svg viewBox="12 12 104 104"`) != 2 ||
				!strings.Contains(svg, `data-wx="move"`) || !strings.Contains(svg, `data-wx="still"`) {
				t.Errorf("%s (day %v): %.80s", c, day, svg)
			}
		}
	}
	if !strings.Contains(weatherIcon("", true, ""), `id="not-available"`) {
		t.Error("an unknown condition should show not-available")
	}
}
