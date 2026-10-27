package app_test

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"mime/multipart"
	"net/http"
	"os"
	"strings"
	"testing"

	"image/jpeg"

	"github.com/DeRusto/goCasa/internal/apptest"
)

func uploadAvatar(t *testing.T, h *apptest.Harness, id string, file []byte) int {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, _ := mw.CreateFormFile("avatar", "me.png")
	fw.Write(file)
	mw.Close()
	req, _ := http.NewRequest(http.MethodPost, h.Server.URL+"/settings/members/"+id+"/avatar", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	res, err := h.Client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	return res.StatusCode
}

func TestUploadedAvatarIsA256PixelSquareElseInitials(t *testing.T) {
	t.Parallel()
	h := apptest.New(t)
	setup(t, h)
	h.Post(t, "/settings/members", member("Rex", "#22c55e", "member", "")) // id 2

	img := image.NewRGBA(image.Rect(0, 0, 1200, 800))
	for y := 0; y < 800; y++ {
		for x := 0; x < 1200; x++ {
			img.Set(x, y, color.RGBA{200, 30, 30, 255})
		}
	}
	var pic bytes.Buffer
	png.Encode(&pic, img)
	if code := uploadAvatar(t, h, "2", pic.Bytes()); code != http.StatusOK {
		t.Fatalf("upload = %d", code)
	}
	if code := uploadAvatar(t, h, "2", []byte("not a picture")); code != http.StatusBadRequest {
		t.Fatalf("upload junk = %d, want 400", code)
	}

	res, err := h.Client.Get(h.Server.URL + "/members/2/avatar")
	if err != nil {
		t.Fatal(err)
	}
	got, _, err := image.Decode(res.Body)
	res.Body.Close()
	if err != nil || got.Bounds().Dx() != 256 || got.Bounds().Dy() != 256 {
		t.Fatalf("avatar = %v, %v; want 256x256", got.Bounds(), err)
	}
	if r, g, _, _ := got.At(128, 128).RGBA(); r>>8 < 180 || g>>8 > 60 {
		t.Fatalf("avatar colour = %v, want the uploaded red", got.At(128, 128))
	}
	if _, body := h.Get(t, "/settings"); !strings.Contains(body, `src="/members/2/avatar`) {
		t.Fatal("Settings doesn't show the uploaded avatar")
	}

	h.Post(t, "/settings/members/2/avatar/delete", nil)
	if code, _ := h.Get(t, "/members/2/avatar"); code != http.StatusNotFound {
		t.Fatalf("removed avatar = %d, want 404", code)
	}
	if _, body := h.Get(t, "/settings"); strings.Contains(body, `src="/members/2/avatar`) || !strings.Contains(body, ">R</span>") {
		t.Fatal("removed avatar didn't fall back to initials")
	}
}

func TestPhoneJPEGAvatarIsTurnedUpright(t *testing.T) {
	t.Parallel()
	h := apptest.New(t)
	setup(t, h)
	// Stored sideways: red left half, blue right half. EXIF orientation 6 says
	// turn it 90° clockwise to view, which puts red on top.
	img := image.NewRGBA(image.Rect(0, 0, 400, 200))
	for y := 0; y < 200; y++ {
		for x := 0; x < 400; x++ {
			c := color.RGBA{220, 20, 20, 255}
			if x >= 200 {
				c = color.RGBA{20, 20, 220, 255}
			}
			img.Set(x, y, c)
		}
	}
	var raw bytes.Buffer
	jpeg.Encode(&raw, img, nil)
	exif := []byte("Exif\x00\x00MM\x00\x2a\x00\x00\x00\x08\x00\x01\x01\x12\x00\x03\x00\x00\x00\x01\x00\x06\x00\x00\x00\x00\x00\x00")
	pic := append([]byte{0xff, 0xd8, 0xff, 0xe1, 0, byte(len(exif) + 2)}, exif...)
	pic = append(pic, raw.Bytes()[2:]...)
	if code := uploadAvatar(t, h, "1", pic); code != http.StatusOK {
		t.Fatalf("upload = %d", code)
	}

	res, err := h.Client.Get(h.Server.URL + "/members/1/avatar")
	if err != nil {
		t.Fatal(err)
	}
	got, _, err := image.Decode(res.Body)
	res.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if r, _, b, _ := got.At(128, 40).RGBA(); r>>8 < 150 || b>>8 > 100 {
		t.Fatalf("top = %v, want red", got.At(128, 40))
	}
	if r, _, b, _ := got.At(128, 216).RGBA(); b>>8 < 150 || r>>8 > 100 {
		t.Fatalf("bottom = %v, want blue", got.At(128, 216))
	}
}

func TestWebPAvatarUploads(t *testing.T) {
	t.Parallel()
	h := apptest.New(t)
	setup(t, h)
	pic, err := os.ReadFile("testdata/blue-purple-pink.lossy.webp") // from golang.org/x/image/testdata
	if err != nil {
		t.Fatal(err)
	}
	if code := uploadAvatar(t, h, "1", pic); code != http.StatusOK {
		t.Fatalf("WebP upload = %d", code)
	}
	if code, _ := h.Get(t, "/members/1/avatar"); code != http.StatusOK {
		t.Fatalf("WebP avatar = %d", code)
	}
}
