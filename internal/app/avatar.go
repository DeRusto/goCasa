package app

import (
	"bytes"
	"database/sql"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"strconv"

	_ "golang.org/x/image/webp"
)

const (
	avatarPx = 256
	// Under ParseMultipartForm's 32 MB memory limit, so an upload never needs a
	// temp file (the scratch image has no /tmp).
	maxAvatarUpload = 20 << 20
	maxAvatarPixels = 50_000_000 // refuse decompression bombs before decoding
)

// avatarJPEG crops the picture to a centred square, turns it upright per its
// EXIF orientation, and downscales it to avatarPx, flattening any
// transparency onto white.
func avatarJPEG(r io.ReadSeeker) ([]byte, error) {
	cfg, _, err := image.DecodeConfig(r)
	if err != nil {
		return nil, err
	}
	if cfg.Width*cfg.Height > maxAvatarPixels {
		return nil, errors.New("picture too large")
	}
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	head := make([]byte, 64<<10) // the EXIF segment is at most 64 KB, near the start
	n, _ := io.ReadFull(r, head)
	orientation := exifOrientation(head[:n])
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	src, _, err := image.Decode(r)
	if err != nil {
		return nil, err
	}
	b := src.Bounds()
	side := min(b.Dx(), b.Dy())
	x0, y0 := b.Min.X+(b.Dx()-side)/2, b.Min.Y+(b.Dy()-side)/2
	// Upright (u, v) in the centred square -> stored (x, y). Rotating a centred
	// square maps it onto itself, so the crop needs no change.
	e := side - 1
	stored := map[int]func(u, v int) (int, int){
		2: func(u, v int) (int, int) { return e - u, v },
		3: func(u, v int) (int, int) { return e - u, e - v },
		4: func(u, v int) (int, int) { return u, e - v },
		5: func(u, v int) (int, int) { return v, u },
		6: func(u, v int) (int, int) { return v, e - u },
		7: func(u, v int) (int, int) { return e - v, e - u },
		8: func(u, v int) (int, int) { return e - v, u },
	}[orientation]
	if stored == nil {
		stored = func(u, v int) (int, int) { return u, v }
	}
	dst := image.NewRGBA(image.Rect(0, 0, avatarPx, avatarPx))
	// Box filter: each output pixel averages the source pixels it covers.
	for y := 0; y < avatarPx; y++ {
		sy0, sy1 := y*side/avatarPx, max((y+1)*side/avatarPx, y*side/avatarPx+1)
		for x := 0; x < avatarPx; x++ {
			sx0, sx1 := x*side/avatarPx, max((x+1)*side/avatarPx, x*side/avatarPx+1)
			var rs, gs, bs, n uint64
			for sy := sy0; sy < sy1; sy++ {
				for sx := sx0; sx < sx1; sx++ {
					px, py := stored(sx, sy)
					r, g, bl, a := src.At(x0+px, y0+py).RGBA() // alpha-premultiplied
					rs += uint64(r + 0xffff - a)
					gs += uint64(g + 0xffff - a)
					bs += uint64(bl + 0xffff - a)
					n++
				}
			}
			dst.SetRGBA(x, y, color.RGBA{uint8(rs / n >> 8), uint8(gs / n >> 8), uint8(bs / n >> 8), 0xff})
		}
	}
	var out bytes.Buffer
	if err := jpeg.Encode(&out, dst, &jpeg.Options{Quality: 85}); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// exifOrientation is the EXIF orientation (1-8) in a JPEG's start, or 0 when
// there is none: phones store photos sideways and say how to turn them.
func exifOrientation(b []byte) int {
	if len(b) < 4 || b[0] != 0xff || b[1] != 0xd8 {
		return 0
	}
	for i := 2; i+4 <= len(b) && b[i] == 0xff; {
		marker, size := b[i+1], int(binary.BigEndian.Uint16(b[i+2:]))
		if size < 2 {
			return 0
		}
		seg := b[i+4 : min(i+2+size, len(b))]
		if marker == 0xda { // image data: no more metadata
			return 0
		}
		if marker == 0xe1 && len(seg) >= 14 && string(seg[:4]) == "Exif" && seg[4] == 0 && seg[5] == 0 {
			tiff := seg[6:]
			var order binary.ByteOrder = binary.BigEndian
			if string(tiff[:2]) == "II" {
				order = binary.LittleEndian
			}
			ifd := int(order.Uint32(tiff[4:]))
			if ifd+2 > len(tiff) {
				return 0
			}
			for j, n := 0, int(order.Uint16(tiff[ifd:])); j < n; j++ {
				e := ifd + 2 + 12*j
				if e+12 > len(tiff) {
					return 0
				}
				if order.Uint16(tiff[e:]) == 0x0112 {
					return int(order.Uint16(tiff[e+8:]))
				}
			}
			return 0
		}
		i += 2 + size
	}
	return 0
}

func (a *App) uploadAvatar(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	r.Body = http.MaxBytesReader(w, r.Body, maxAvatarUpload)
	f, _, err := r.FormFile("avatar")
	if err != nil {
		a.settingsPage(w, r, http.StatusBadRequest, "Pick a picture under 20 MB.")
		return
	}
	defer f.Close()
	pic, err := avatarJPEG(f)
	if err != nil {
		a.settingsPage(w, r, http.StatusBadRequest, "Use a JPEG, PNG, GIF or WebP picture.")
		return
	}
	a.setAvatar(w, r, id, pic)
}

func (a *App) deleteAvatar(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	a.setAvatar(w, r, id, nil)
}

func (a *App) setAvatar(w http.ResponseWriter, r *http.Request, id int64, pic []byte) {
	res, err := a.DB.ExecContext(r.Context(), "UPDATE members SET avatar = ? WHERE id = ?", pic, id)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		http.NotFound(w, r)
		return
	}
	a.publish("updated", "member", id)
	http.Redirect(w, r, "/settings", http.StatusSeeOther)
}

// getAvatar serves a Member's picture to every device, locked or not.
func (a *App) getAvatar(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	var pic []byte
	err := a.DB.QueryRowContext(r.Context(), "SELECT avatar FROM members WHERE id = ? AND avatar IS NOT NULL", id).Scan(&pic)
	if errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return
	} else if err != nil {
		a.serverError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "no-cache")
	w.Write(pic)
}
