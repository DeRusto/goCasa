package app

import (
	"context"
	"database/sql"
	"errors"
	"io/fs"
	"maps"
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
	var found []immichAsset // ID is the slash-separated path inside the folder
	err := filepath.WalkDir(a.folderDir(), func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
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
		found = append(found, immichAsset{ID: filepath.ToSlash(rel), TakenAt: info.ModTime()})
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

// screensaverPhotos is the photos the screensaver rotates through, each with
// whether its bytes are at hand: Immich's, else the Fallback folder's when it
// is on. Immich has nothing to show when not connected, its list is empty,
// or it is failing with none of its photos cached. Never both.
func (a *App) screensaverPhotos(ctx context.Context) (map[int64]bool, error) {
	im, err := a.photoList(ctx, photoSourceImmich)
	if err != nil {
		return nil, err
	}
	if len(im) > 0 {
		var failing bool
		if err := a.DB.QueryRowContext(ctx, "SELECT consecutive_failures > 0 FROM integrations WHERE type = ?", immichType).Scan(&failing); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		if failing { // only cached bytes can show, so rotate through just those
			maps.DeleteFunc(im, func(_ int64, c bool) bool { return !c })
		}
		if len(im) > 0 {
			return im, nil
		}
	}
	var on bool
	if err := a.DB.QueryRowContext(ctx, "SELECT fallback_folder_enabled FROM household_settings").Scan(&on); err != nil || !on {
		return nil, err
	}
	folder, err := a.photoList(ctx, photoSourceFolder)
	for id := range folder {
		folder[id] = true // on disk already
	}
	return folder, err
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
	w.Header().Set("Cache-Control", "private, max-age=86400")
	http.ServeContent(w, r, rel, time.Time{}, f) // typed by its extension
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
	if _, err := a.DB.ExecContext(r.Context(), "UPDATE household_settings SET fallback_folder_enabled = ?", r.PostForm.Has("enabled")); err != nil {
		a.serverError(w, r, err)
		return
	}
	http.Redirect(w, r, "/settings", http.StatusSeeOther)
}

// fallbackSettings is the Fallback folder section of Settings.
func (a *App) fallbackSettings(ctx context.Context) (web.Fallback, error) {
	var f web.Fallback
	err := a.DB.QueryRowContext(ctx, "SELECT fallback_folder_enabled, (SELECT count(*) FROM photos_cache WHERE source = 'folder') FROM household_settings").Scan(&f.Enabled, &f.Images)
	return f, err
}
