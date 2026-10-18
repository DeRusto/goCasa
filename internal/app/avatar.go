package app

import (
	"bytes"
	"database/sql"
	"errors"
	"image"
	"image/color"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"strconv"
)

const (
	avatarPx = 256
	// Under ParseMultipartForm's 32 MB memory limit, so an upload never needs a
	// temp file (the scratch image has no /tmp).
	maxAvatarUpload = 20 << 20
	maxAvatarPixels = 50_000_000 // refuse decompression bombs before decoding
)

// avatarJPEG crops the picture to a centred square and downscales it to
// avatarPx, flattening any transparency onto white.
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
	src, _, err := image.Decode(r)
	if err != nil {
		return nil, err
	}
	b := src.Bounds()
	side := min(b.Dx(), b.Dy())
	x0, y0 := b.Min.X+(b.Dx()-side)/2, b.Min.Y+(b.Dy()-side)/2
	dst := image.NewRGBA(image.Rect(0, 0, avatarPx, avatarPx))
	// Box filter: each output pixel averages the source pixels it covers.
	for y := 0; y < avatarPx; y++ {
		sy0, sy1 := y*side/avatarPx, max((y+1)*side/avatarPx, y*side/avatarPx+1)
		for x := 0; x < avatarPx; x++ {
			sx0, sx1 := x*side/avatarPx, max((x+1)*side/avatarPx, x*side/avatarPx+1)
			var rs, gs, bs, n uint64
			for sy := sy0; sy < sy1; sy++ {
				for sx := sx0; sx < sx1; sx++ {
					r, g, bl, a := src.At(x0+sx, y0+sy).RGBA() // alpha-premultiplied
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
		a.settingsPage(w, r, http.StatusBadRequest, "Use a JPEG, PNG or GIF picture.")
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
