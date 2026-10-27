package app_test

import (
	"bytes"
	"image"
	"image/jpeg"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DeRusto/goCasa/internal/apptest"
)

// addFolderPhoto writes body as rel inside the Household's Fallback folder.
func addFolderPhoto(t *testing.T, h *apptest.Harness, rel, body string) {
	t.Helper()
	p := filepath.Join(h.App.Config.DataDir, "photos", filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o640); err != nil {
		t.Fatal(err)
	}
}

func TestTheFallbackFolderShowsWithoutImmich(t *testing.T) {
	t.Parallel()
	h := apptest.New(t)
	setup(t, h)
	wall := h.Device()
	addFolderPhoto(t, h, "beach.jpg", "jpeg:beach")
	addFolderPhoto(t, h, "2024/Trip/hike.WEBP", "webp:hike")
	addFolderPhoto(t, h, "garden.png", "png:garden")
	addFolderPhoto(t, h, "notes.txt", "not a photo")
	addFolderPhoto(t, h, "clip.mov", "not a photo")
	h.RunDue()

	seen := map[string]bool{}
	for range 3 {
		p := photo(t, wall)
		if p == "" || seen[p] {
			t.Fatalf("photo %q after %v: missing or repeated", p, seen)
		}
		code, img := wall.Get(t, p)
		if code != http.StatusOK {
			t.Fatalf("%s = %d %q", p, code, img)
		}
		seen[img] = true
		seen[p] = true
		h.Clock.Advance(30 * time.Second)
	}
	for _, want := range []string{"jpeg:beach", "webp:hike", "png:garden"} {
		if !seen[want] {
			t.Errorf("the rotation never served %s: %v", want, seen)
		}
	}
	if _, page := h.Get(t, "/settings"); !strings.Contains(page, "Images in the Fallback folder: 3") {
		t.Errorf("Settings doesn't count the folder's images: %q", page)
	}
}

// A photo replaced in place keeps its address, so browsers must check back
// rather than show cached bytes for a day.
func TestAFallbackPhotoReplacedInPlaceIsRefetched(t *testing.T) {
	t.Parallel()
	h := apptest.New(t)
	setup(t, h)
	wall := h.Device()
	addFolderPhoto(t, h, "beach.jpg", "jpeg:beach")
	h.RunDue()
	p := photo(t, wall)
	res, err := wall.Client.Get(wall.Server.URL + p)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if cc := res.Header.Get("Cache-Control"); !strings.Contains(cc, "no-cache") || res.Header.Get("Last-Modified") == "" {
		t.Fatalf("Cache-Control %q, Last-Modified %q: a replaced photo can show stale", cc, res.Header.Get("Last-Modified"))
	}
	addFolderPhoto(t, h, "beach.jpg", "jpeg:beach at sunset")
	if _, img := wall.Get(t, p); img != "jpeg:beach at sunset" {
		t.Fatalf("replaced photo = %q", img)
	}
}

func TestTheFallbackFolderIsNeverMixedWithImmich(t *testing.T) {
	t.Parallel()
	h, _ := immichHousehold(t)
	wall := h.Device()
	addFolderPhoto(t, h, "beach.jpg", "jpeg:beach")
	post(t, h, "/settings/immich/picks", url.Values{"album:alb-summer": {"show"}})
	h.RunDue()
	for range 6 {
		_, img := wall.Get(t, photo(t, wall))
		if !strings.HasPrefix(img, "img:") {
			t.Fatalf("Immich has photos, yet the screensaver showed %q", img)
		}
		h.Clock.Advance(30 * time.Second)
	}

	post(t, h, "/settings/immich/picks", nil)
	if _, img := wall.Get(t, photo(t, wall)); img != "jpeg:beach" {
		t.Errorf("Immich's set is empty, yet the screensaver showed %q", img)
	}
}

func TestTheFallbackFolderShowsWhileImmichIsDownWithNothingCached(t *testing.T) {
	t.Parallel()
	h, f := immichHousehold(t)
	wall := h.Device()
	addFolderPhoto(t, h, "beach.jpg", "jpeg:beach")
	post(t, h, "/settings/immich/picks", url.Values{"album:alb-summer": {"show"}})
	f.setDown(true)
	h.Clock.Advance(time.Hour)
	h.RunDue() // the re-list fails with nothing cached
	if _, img := wall.Get(t, photo(t, wall)); img != "jpeg:beach" {
		t.Errorf("Immich is down with an empty cache, yet the screensaver showed %q", img)
	}
	f.setDown(false)
	h.Clock.Advance(time.Hour)
	h.RunDue()
	h.Clock.Advance(30 * time.Second)
	if _, img := wall.Get(t, photo(t, wall)); !strings.HasPrefix(img, "img:") {
		t.Errorf("Immich is back, yet the screensaver showed %q", img)
	}
}

func TestTheFallbackFolderRescansHourlyAndOnRefreshNow(t *testing.T) {
	t.Parallel()
	h := apptest.New(t)
	setup(t, h)
	wall := h.Device()
	h.RunDue()
	addFolderPhoto(t, h, "beach.jpg", "jpeg:beach")
	h.Clock.Advance(59 * time.Minute)
	h.RunDue()
	if p := photo(t, wall); p != "" {
		t.Fatalf("rescanned before the hour: %s", p)
	}
	h.Clock.Advance(time.Minute)
	h.RunDue()
	if _, img := wall.Get(t, photo(t, wall)); img != "jpeg:beach" {
		t.Fatalf("the hourly rescan missed beach.jpg: %q", img)
	}

	unlock(t, h, 1, "1234") // the hour's wait ended the unlock
	os.Remove(filepath.Join(h.App.Config.DataDir, "photos", "beach.jpg"))
	addFolderPhoto(t, h, "kids/park.png", "png:park")
	post(t, h, "/settings/fallback-folder/refresh", nil)
	if _, img := wall.Get(t, photo(t, wall)); img != "png:park" {
		t.Errorf("after Refresh now the screensaver showed %q", img)
	}
	if _, page := h.Get(t, "/settings"); !strings.Contains(page, "Images in the Fallback folder: 1") {
		t.Errorf("Settings doesn't count the rescanned folder: %q", page)
	}
}

func TestWithNeitherSourceTheClockAndAgendaShowOnDark(t *testing.T) {
	t.Parallel()
	h := apptest.New(t)
	setup(t, h)
	wall := h.Device()
	addFolderPhoto(t, h, "beach.jpg", "jpeg:beach")
	post(t, h, "/settings/fallback-folder", nil) // switched off
	if _, page := h.Get(t, "/settings"); !strings.Contains(page, `name="enabled">`) {
		t.Errorf("Settings doesn't show the Fallback folder off: %q", page)
	}
	h.RunDue()
	_, page := wall.Get(t, "/screensaver")
	if photoRE.MatchString(page) || !strings.Contains(page, ">09:00</time>") || !strings.Contains(page, "background:#000") {
		t.Errorf("with the Fallback folder off and no Immich, the screensaver = %q", page)
	}

	post(t, h, "/settings/fallback-folder", url.Values{"enabled": {"on"}})
	if p := photo(t, wall); p == "" {
		t.Errorf("the Fallback folder is back on, yet no photo shows")
	}
}

func TestSidewaysFolderJPEGsPairAsPortraits(t *testing.T) {
	t.Parallel()
	h := apptest.New(t)
	setup(t, h)
	wall := h.Device()
	// Stored 400×200 with EXIF orientation 6: upright, a 200×400 portrait.
	pic := sidewaysJPEG(400, 200)
	addFolderPhoto(t, h, "a.jpg", pic)
	addFolderPhoto(t, h, "b.jpg", pic)
	addFolderPhoto(t, h, "broken.jpg", "not a jpeg") // no size: shows alone
	h.RunDue()

	sizes := map[int]int{}
	for range 2 {
		sizes[len(step(t, wall, ""))]++
		h.Clock.Advance(30 * time.Second)
	}
	if sizes[2] != 1 || sizes[1] != 1 {
		t.Fatalf("steps by photo count = %v, want the two sideways JPEGs paired and the broken one alone", sizes)
	}
}

func TestLargeFolderPhotosShowAsScreenSizedCopies(t *testing.T) {
	t.Parallel()
	h := apptest.New(t)
	setup(t, h)
	wall := h.Device()
	addFolderPhoto(t, h, "tall.jpg", sidewaysJPEG(4000, 1000)) // upright 1000×4000
	h.RunDue()

	ps := step(t, wall, "")
	if len(ps) != 1 {
		t.Fatalf("step = %v", ps)
	}
	_, body := wall.Get(t, ps[0].Src)
	cfg, err := jpeg.DecodeConfig(strings.NewReader(body))
	if err != nil || cfg.Width != 480 || cfg.Height != 1920 {
		t.Fatalf("a sideways 4000x1000 photo shows as %dx%d (%v), want upright 480x1920", cfg.Width, cfg.Height, err)
	}

	os.Remove(filepath.Join(h.App.Config.DataDir, "photos", "tall.jpg"))
	post(t, h, "/settings/fallback-folder/refresh", nil)
	if left, _ := os.ReadDir(filepath.Join(h.App.Config.DataDir, "photos-screen")); len(left) != 0 {
		t.Errorf("copies of removed photos stay: %v", left)
	}
}

// sidewaysJPEG is a w×h JPEG whose EXIF orientation 6 says to view it turned
// a quarter clockwise, as a phone stores a portrait.
func sidewaysJPEG(w, h int) string {
	var raw bytes.Buffer
	jpeg.Encode(&raw, image.NewGray(image.Rect(0, 0, w, h)), nil)
	exif := []byte("Exif\x00\x00MM\x00\x2a\x00\x00\x00\x08\x00\x01\x01\x12\x00\x03\x00\x00\x00\x01\x00\x06\x00\x00\x00\x00\x00\x00")
	pic := append([]byte{0xff, 0xd8, 0xff, 0xe1, 0, byte(len(exif) + 2)}, exif...)
	return string(append(pic, raw.Bytes()[2:]...))
}
