package app_test

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"mime/multipart"
	"net/http"
	"strings"
	"testing"

	_ "image/jpeg"

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
