package app

import (
	"context"
	"errors"
	"image"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/DeRusto/goCasa/internal/web"
)

// The Fallback folder (spec §10): the Household's own images at
// DATA_DIR/photos, shown only when Immich has nothing to show.

const folderRescan = time.Hour

func (a *App) folderDir() string { return filepath.Join(a.Config.DataDir, "photos") }

// folderScanDue says the hourly rescan is due.
func (a *App) folderScanDue(now time.Time) bool {
	a.folderMu.Lock()
	defer a.folderMu.Unlock()
	return a.folderScanned.IsZero() || now.Sub(a.folderScanned) >= folderRescan
}

// scanFolder lists the folder's JPEG, PNG and WebP images, subfolders
// included, as the folder's rotation list. A missing folder lists nothing.
func (a *App) scanFolder(ctx context.Context, now time.Time) error {
	type known struct {
		taken int64
		w, h  int
	}
	seen := map[string]known{} // what the last scan stored, so unchanged files aren't reread
	rows, err := a.DB.QueryContext(ctx, "SELECT external_id_or_path, coalesce(taken_at, 0), width, height FROM photos_cache WHERE source = ?", photoSourceFolder)
	if err != nil {
		return err
	}
	for rows.Next() {
		var rel string
		var k known
		if err := rows.Scan(&rel, &k.taken, &k.w, &k.h); err != nil {
			rows.Close()
			return err
		}
		seen[rel] = k
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	loc := a.location(ctx)
	var found []immichAsset // ID is the slash-separated path inside the folder
	err = filepath.WalkDir(a.folderDir(), func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			if p == a.folderDir() {
				return err
			}
			a.Log.Warn("fallback folder: skipping unreadable entry", "path", p, "err", err)
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		switch strings.ToLower(filepath.Ext(p)) {
		case ".jpg", ".jpeg", ".png", ".webp":
		default:
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil // gone since the listing
		}
		rel, err := filepath.Rel(a.folderDir(), p)
		if err != nil {
			return err
		}
		// Local, as Immich has it: the mod time's wall clock in the Household's zone, written as UTC.
		mod := info.ModTime().In(loc)
		as := immichAsset{ID: filepath.ToSlash(rel), Local: time.Date(mod.Year(), mod.Month(), mod.Day(), mod.Hour(), mod.Minute(), mod.Second(), 0, time.UTC)}
		if k, ok := seen[as.ID]; ok && k.taken == as.Local.Unix() && k.w > 0 {
			as.Width, as.Height = k.w, k.h
		} else {
			as.Width, as.Height = imageSize(p)
		}
		found = append(found, as)
		return nil
	})
	if err != nil {
		return err
	}
	if err := a.replacePhotos(ctx, photoSourceFolder, found); err != nil {
		return err
	}
	a.folderMu.Lock()
	a.folderScanned = now
	a.folderMu.Unlock()
	return nil
}

// imageSize is image file p's upright width and height from its header
// (EXIF orientation for a JPEG); 0 when unreadable.
func imageSize(p string) (int, int) {
	f, err := os.Open(p)
	if err != nil {
		return 0, 0
	}
	defer f.Close()
	head := make([]byte, 64<<10) // the EXIF segment is at most 64 KB, near the start
	n, _ := io.ReadFull(f, head)
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return 0, 0
	}
	cfg, _, err := image.DecodeConfig(f) // reads only up to the size, which may sit past head
	if err != nil {
		return 0, 0
	}
	if o := exifOrientation(head[:n]); o >= 5 { // turned a quarter
		return cfg.Height, cfg.Width
	}
	return cfg.Width, cfg.Height
}

// folderPhoto is Fallback folder photo id's path inside the folder.
func (a *App) folderPhoto(ctx context.Context, id int64) (string, error) {
	var rel string
	if err := a.DB.QueryRowContext(ctx, "SELECT external_id_or_path FROM photos_cache WHERE id = ? AND source = 'folder'", id).Scan(&rel); err != nil {
		return "", err
	}
	return rel, nil
}

// serveFolderPhoto serves rel from the Fallback folder, never outside it.
func (a *App) serveFolderPhoto(w http.ResponseWriter, r *http.Request, rel string) {
	root, err := os.OpenRoot(a.folderDir())
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer root.Close()
	f, err := root.Open(filepath.FromSlash(rel))
	if err != nil {
		http.NotFound(w, r) // removed since the last scan
		return
	}
	defer f.Close()
	var mod time.Time
	if info, err := f.Stat(); err == nil {
		mod = info.ModTime()
	}
	// A file replaced in place keeps its URL: browsers check back, and get a 304 while it's unchanged.
	w.Header().Set("Cache-Control", "private, no-cache")
	http.ServeContent(w, r, rel, mod, f) // typed by its extension
}

// refreshFolder rescans the Fallback folder now ("Refresh now").
func (a *App) refreshFolder(w http.ResponseWriter, r *http.Request) {
	if err := a.scanFolder(context.WithoutCancel(r.Context()), a.Now()); err != nil {
		a.serverError(w, r, err)
		return
	}
	http.Redirect(w, r, "/settings", http.StatusSeeOther)
}

// saveFallbackFolder switches the Fallback folder on or off.
func (a *App) saveFallbackFolder(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	if _, err := a.exec(r.Context(), web.KindHousehold, "UPDATE household_settings SET fallback_folder_enabled = ?", r.PostForm.Has("enabled")); err != nil {
		a.serverError(w, r, err)
		return
	}
	http.Redirect(w, r, "/settings", http.StatusSeeOther)
}

// fallbackSettings is the Fallback folder section of Settings.
func (a *App) fallbackSettings(ctx context.Context) (web.Fallback, error) {
	hh, err := a.household(ctx)
	if err != nil {
		return web.Fallback{}, err
	}
	f := web.Fallback{Enabled: hh.FallbackFolder}
	err = a.DB.QueryRowContext(ctx, "SELECT count(*) FROM photos_cache WHERE source = 'folder'").Scan(&f.Images)
	return f, err
}
