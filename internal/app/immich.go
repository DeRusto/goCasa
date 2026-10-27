package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/DeRusto/goCasa/internal/web"
	"github.com/a-h/templ"
)

// immich lists the photos of the albums and people an Admin picked (spec
// §10) into photos_cache for the screensaver. Its credentials are the API key,
// its config_json the address, what can be picked and what is.
type immich struct{}

const (
	immichType        = "immich"
	photoSourceImmich = "immich" // photos_cache.source
	photoSourceFolder = "folder"
	immichRelist      = 3600 // seconds between re-lists
	immichPrefetch    = 3    // photos fetched ahead of the screensaver
)

var (
	immichClient    = feedClient
	photoCacheLimit = int64(500 << 20) // bytes of proxied photos kept on disk
)

type immichConfig struct {
	URL          string        `json:"url"`
	Albums       []immichNamed `json:"albums"` // what can be picked, as of the last sync
	People       []immichNamed `json:"people"`
	PickedAlbums []string      `json:"picked_albums"`
	PickedPeople []string      `json:"picked_people"`
}

type immichNamed struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type immichConn struct {
	base string
	sc   syncClient
}

func newImmichConn(base, key string) immichConn {
	refused := errors.New("Immich refused the API key: it needs asset.read, asset.view, album.read and person.read")
	return immichConn{base, syncClient{name: "Immich", client: immichClient,
		header: http.Header{"X-Api-Key": {key}, "Accept": {"application/json"}}, // never ?apiKey=, which ends up in logs
		limit:  50 << 20, overrides: map[int]error{http.StatusUnauthorized: refused, http.StatusForbidden: refused}}}
}

// call sends body (if any) as JSON to Immich and decodes its answer into out.
func (c immichConn) call(ctx context.Context, method, path string, body, out any) error {
	return c.sc.json(ctx, method, c.base+path, body, out)
}

func (c immichConn) do(ctx context.Context, method, path string, body any) (*http.Response, error) {
	return c.sc.do(ctx, method, c.base+path, body)
}

func (immich) Sync(ctx context.Context, a *App, in integrationRow) error {
	cfg, err := a.immichConfig(ctx, in.id)
	if err != nil {
		return err
	}
	c := newImmichConn(cfg.URL, string(in.credentials))
	albums, people, err := c.pickable(ctx)
	if err != nil {
		return err
	}
	ab, _ := json.Marshal(albums)
	pb, _ := json.Marshal(people)
	if _, err := a.DB.ExecContext(ctx, "UPDATE integrations SET config_json = json_set(config_json, '$.albums', json(?), '$.people', json(?)) WHERE id = ?", string(ab), string(pb), in.id); err != nil {
		return err
	}
	var assets []immichAsset
	if len(cfg.PickedAlbums)+len(cfg.PickedPeople) > 0 { // nothing picked = no Immich photos, never the whole library
		if assets, err = c.search(ctx, cfg.PickedAlbums, cfg.PickedPeople); err != nil {
			return err
		}
	}
	return a.replacePhotos(ctx, photoSourceImmich, assets)
}

// pickable is the albums and named people the key's user can see.
func (c immichConn) pickable(ctx context.Context) (albums, people []immichNamed, err error) {
	var as []struct {
		ID   string `json:"id"`
		Name string `json:"albumName"`
	}
	if err := c.call(ctx, http.MethodGet, "/api/albums", nil, &as); err != nil {
		return nil, nil, err
	}
	for _, x := range as {
		albums = append(albums, immichNamed{x.ID, x.Name})
	}
	for page := 1; ; page++ {
		var ps struct {
			People []struct {
				ID     string `json:"id"`
				Name   string `json:"name"`
				Hidden bool   `json:"isHidden"`
			} `json:"people"`
			More bool `json:"hasNextPage"`
		}
		if err := c.call(ctx, http.MethodGet, "/api/people?size=1000&page="+strconv.Itoa(page), nil, &ps); err != nil {
			return nil, nil, err
		}
		for _, p := range ps.People {
			if p.Name != "" && !p.Hidden { // unnamed faces can't be told apart in a picker
				people = append(people, immichNamed{p.ID, p.Name})
			}
		}
		if !ps.More || page >= 100 {
			return albums, people, nil
		}
	}
}

type immichAsset struct {
	ID      string    `json:"id"`
	TakenAt time.Time `json:"fileCreatedAt"`
}

// search lists the photos in any of albums or showing any of people,
// leaving out trashed, archived, hidden and non-image assets: v3.2's filter
// shape excludes none of them by default.
func (c immichConn) search(ctx context.Context, albums, people []string) ([]immichAsset, error) {
	var or []any
	if len(albums) > 0 {
		or = append(or, map[string]any{"albumIds": map[string]any{"any": albums}})
	}
	if len(people) > 0 {
		or = append(or, map[string]any{"personIds": map[string]any{"any": people}})
	}
	body := map[string]any{
		"filter": map[string]any{
			"or":         or,
			"trashedAt":  map[string]any{"eq": nil},
			"visibility": map[string]any{"eq": "timeline"},
			"type":       map[string]any{"eq": "IMAGE"},
		},
		"size": 1000,
	}
	var all []immichAsset
	for range 1000 { // at most a million photos
		var res struct {
			Assets struct {
				Items      []immichAsset `json:"items"`
				NextCursor *string       `json:"nextCursor"`
			} `json:"assets"`
		}
		if err := c.call(ctx, http.MethodPost, "/api/search/metadata", body, &res); err != nil {
			return nil, err
		}
		all = append(all, res.Assets.Items...)
		if res.Assets.NextCursor == nil {
			break
		}
		body["cursor"] = *res.Assets.NextCursor
	}
	return all, nil
}

// replacePhotos makes source's rotation list exactly assets, keeping the
// cached bytes of those still in it.
func (a *App) replacePhotos(ctx context.Context, source string, assets []immichAsset) error {
	var gone []string
	err := a.inTx(ctx, func(tx *sql.Tx) error {
		keep := map[string]bool{}
		for _, as := range assets {
			keep[as.ID] = true
			if _, err := tx.ExecContext(ctx, `INSERT INTO photos_cache (source, external_id_or_path, taken_at) VALUES (?, ?, ?)
				ON CONFLICT (source, external_id_or_path) DO UPDATE SET taken_at = excluded.taken_at`, source, as.ID, as.TakenAt.Unix()); err != nil {
				return err
			}
		}
		rows, err := tx.QueryContext(ctx, "SELECT id, external_id_or_path, coalesce(cache_path, '') FROM photos_cache WHERE source = ?", source)
		if err != nil {
			return err
		}
		var drop []int64
		for rows.Next() {
			var id int64
			var ext, path string
			if err := rows.Scan(&id, &ext, &path); err != nil {
				rows.Close()
				return err
			}
			if !keep[ext] {
				drop = append(drop, id)
				gone = append(gone, path)
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, id := range drop {
			if _, err := tx.ExecContext(ctx, "DELETE FROM photos_cache WHERE id = ?", id); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	a.removeCached(gone)
	return nil
}

func (a *App) photoDir() string { return filepath.Join(a.Config.DataDir, "photo-cache") }

func (a *App) removeCached(paths []string) {
	for _, p := range paths {
		if p != "" {
			os.Remove(filepath.Join(a.photoDir(), p))
		}
	}
}

func (a *App) immichConfig(ctx context.Context, id int64) (immichConfig, error) {
	var raw string
	var cfg immichConfig
	if err := a.DB.QueryRowContext(ctx, "SELECT config_json FROM integrations WHERE id = ?", id).Scan(&raw); err != nil {
		return cfg, err
	}
	return cfg, json.Unmarshal([]byte(raw), &cfg)
}

// immichOf is the Immich integration's id and connection; id 0 when not connected.
func (a *App) immichOf(ctx context.Context) (int64, immichConn, error) {
	var id int64
	var enc []byte
	err := a.DB.QueryRowContext(ctx, "SELECT id, credentials_enc FROM integrations WHERE type = ?", immichType).Scan(&id, &enc)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, newImmichConn("", ""), nil // not connected: calls fail, never panic
	}
	if err != nil {
		return 0, immichConn{}, err
	}
	key, err := a.unseal(enc)
	if err != nil {
		return 0, immichConn{}, err
	}
	cfg, err := a.immichConfig(ctx, id)
	return id, newImmichConn(cfg.URL, string(key)), err
}

// connectImmich checks the server is Immich v3.2+ and takes the key, then
// adds it and lists what can be picked.
func (a *App) connectImmich(w http.ResponseWriter, r *http.Request) {
	raw := strings.TrimRight(strings.TrimSpace(r.PostFormValue("url")), "/")
	key := strings.TrimSpace(r.PostFormValue("key"))
	msg, err := a.integrations.Connect(r.Context(), newIntegration{typ: immichType, name: "Immich", url: raw, secret: key,
		missingSecret: "Paste an API key from Immich (Account Settings > API Keys).", cfg: immichConfig{URL: raw}, interval: immichRelist, unique: true,
		check: func(ctx context.Context) string {
			ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			c := newImmichConn(raw, key)
			var v struct{ Major, Minor, Patch int }
			if err := c.call(ctx, http.MethodGet, "/api/server/version", nil, &v); err != nil {
				return "Couldn't reach Immich at that address."
			}
			if v.Major < 3 || v.Major == 3 && v.Minor < 2 {
				return fmt.Sprintf("GoCasa needs Immich v3.2 or later; this one is v%d.%d.%d.", v.Major, v.Minor, v.Patch)
			}
			if _, _, err := c.pickable(ctx); err != nil {
				return "Immich didn't take that API key: " + err.Error()
			}
			return ""
		}})
	a.connected(w, r, msg, err)
}

// disconnectImmich removes Immich, its photo list and their cached bytes.
func (a *App) disconnectImmich(w http.ResponseWriter, r *http.Request) {
	if _, err := a.exec(r.Context(), web.KindIntegration, "DELETE FROM integrations WHERE type = ?", immichType); err != nil {
		a.serverError(w, r, err)
		return
	}
	if err := a.replacePhotos(r.Context(), photoSourceImmich, nil); err != nil {
		a.serverError(w, r, err)
		return
	}
	http.Redirect(w, r, "/settings", http.StatusSeeOther)
}

// saveImmichPicks sets the picked albums and people, then re-lists.
func (a *App) saveImmichPicks(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	albums, _ := json.Marshal(append([]string{}, r.PostForm["album"]...))
	people, _ := json.Marshal(append([]string{}, r.PostForm["person"]...))
	if _, err := a.exec(r.Context(), web.KindIntegration, "UPDATE integrations SET config_json = json_set(config_json, '$.picked_albums', json(?), '$.picked_people', json(?)) WHERE type = ?",
		string(albums), string(people), immichType); err != nil {
		a.serverError(w, r, err)
		return
	}
	a.refreshImmich(w, r)
}

// refreshImmich re-lists the photos now ("Refresh now").
func (a *App) refreshImmich(w http.ResponseWriter, r *http.Request) {
	id, _, err := a.immichOf(r.Context())
	if err == nil && id != 0 {
		_, err = a.integrations.Run(context.WithoutCancel(r.Context()), id) // its outcome shows in Settings
		if err == nil {
			a.prefetchPhotos(context.WithoutCancel(r.Context()), a.Now())
		}
	}
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	http.Redirect(w, r, "/settings", http.StatusSeeOther)
}

func (immich) Section(ctx context.Context, a *App, st map[int64]integrationHealth, _ []web.Member) (templ.Component, error) {
	m, err := a.immichSettings(ctx, st)
	return web.ImmichSection(m), err
}

// immichSettings is the Immich section of Settings; nil when not connected.
func (a *App) immichSettings(ctx context.Context, st map[int64]integrationHealth) (*web.Immich, error) {
	var m web.Immich
	var raw string
	var id int64
	err := a.DB.QueryRowContext(ctx, `SELECT id, config_json FROM integrations WHERE type = ?`, immichType).Scan(&id, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var cfg immichConfig
	json.Unmarshal([]byte(raw), &cfg)
	x := st[id]
	m.URL, m.Failures, m.LastError, m.LastSuccess = cfg.URL, x.Failures, x.LastError, x.LastSuccess
	picked := map[string]bool{}
	for _, id := range append(cfg.PickedAlbums, cfg.PickedPeople...) {
		picked[id] = true
	}
	for _, x := range cfg.Albums {
		m.Albums = append(m.Albums, web.ImmichPick{ID: x.ID, Name: x.Name, Picked: picked[x.ID]})
	}
	for _, x := range cfg.People {
		m.People = append(m.People, web.ImmichPick{ID: x.ID, Name: x.Name, Picked: picked[x.ID]})
	}
	m.NothingPicked = len(picked) == 0
	err = a.DB.QueryRowContext(ctx, "SELECT count(*) FROM photos_cache WHERE source = 'immich'").Scan(&m.Photos)
	return &m, err
}

// photoDeck is the screensaver's no-repeat shuffle of its photos,
// shared by every Wall display: each shows the photo current at the time.
type photoDeck struct {
	mu      sync.Mutex
	order   []int64 // photos_cache ids
	pos     int     // order[pos-1] is showing
	shownAt time.Time
}

// currentPhoto is the photo the screensaver shows now, moving on once it
// has shown for every seconds, and how long it still shows; 0 for none. It
// prefers a cached photo, so rotation continues from the cache while Immich
// is down. The photos are Immich's, else the Fallback folder's.
func (a *App) currentPhoto(ctx context.Context, now time.Time, every time.Duration) (int64, time.Duration, error) {
	d := &a.photos
	d.mu.Lock()
	defer d.mu.Unlock()
	cached, err := a.screensaverPhotos(ctx)
	if err != nil {
		return 0, 0, err
	}
	if d.pos > 0 && now.Sub(d.shownAt) < every {
		if _, ok := cached[d.order[d.pos-1]]; ok {
			return d.order[d.pos-1], every - now.Sub(d.shownAt), nil
		}
	}
	for range 2 {
		if d.pos >= len(d.order) {
			d.order = d.order[:0]
			for id := range cached {
				d.order = append(d.order, id)
			}
			rand.Shuffle(len(d.order), func(i, j int) { d.order[i], d.order[j] = d.order[j], d.order[i] })
			d.pos = 0
		}
		next := -1
		for i := d.pos; i < len(d.order); i++ {
			c, ok := cached[d.order[i]]
			if !ok {
				continue // gone since the shuffle
			}
			if c {
				next = i
				break
			}
			if next < 0 {
				next = i
			}
		}
		if next >= 0 {
			d.pos, d.shownAt = next+1, now
			return d.order[next], every, nil
		}
		d.pos = len(d.order)
	}
	return 0, 0, nil
}

// photoList is every id in source's list and whether its bytes are cached.
func (a *App) photoList(ctx context.Context, source string) (map[int64]bool, error) {
	rows, err := a.DB.QueryContext(ctx, "SELECT id, cache_path IS NOT NULL FROM photos_cache WHERE source = ?", source)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	m := map[int64]bool{}
	for rows.Next() {
		var id int64
		var c bool
		if err := rows.Scan(&id, &c); err != nil {
			return nil, err
		}
		m[id] = c
	}
	return m, rows.Err()
}

// prefetchPhotos caches the next photos the screensaver will show.
func (a *App) prefetchPhotos(ctx context.Context, _ time.Time) error {
	a.photos.mu.Lock()
	next := append([]int64(nil), a.photos.order[a.photos.pos:min(a.photos.pos+immichPrefetch, len(a.photos.order))]...)
	a.photos.mu.Unlock()
	for _, id := range next {
		if _, err := a.cachedPhoto(ctx, id); err != nil && !errors.Is(err, sql.ErrNoRows) {
			a.Log.Warn("prefetching a photo from Immich", "photo", id, "err", err) // the re-list records outages
			return nil
		}
	}
	return nil
}

// cachedPhoto is the file holding photo id's bytes, fetched from Immich if
// not cached yet; the least recently used go once the cache is over its limit.
func (a *App) cachedPhoto(ctx context.Context, id int64) (string, error) {
	var ext string
	var path sql.NullString
	if err := a.DB.QueryRowContext(ctx, "SELECT external_id_or_path, cache_path FROM photos_cache WHERE id = ? AND source = 'immich'", id).Scan(&ext, &path); err != nil {
		return "", err
	}
	now := a.Now().Unix()
	if path.Valid {
		if _, err := os.Stat(filepath.Join(a.photoDir(), path.String)); err != nil {
			path.Valid = false // the cache directory was cleared
		}
	}
	if path.Valid {
		_, err := a.DB.ExecContext(ctx, "UPDATE photos_cache SET last_used_at = ? WHERE id = ?", now, id)
		return filepath.Join(a.photoDir(), path.String), err
	}
	_, c, err := a.immichOf(ctx)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	res, err := c.do(ctx, http.MethodGet, "/api/assets/"+url.PathEscape(ext)+"/thumbnail?size=preview", nil)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	if err := os.MkdirAll(a.photoDir(), 0o750); err != nil {
		return "", err
	}
	name := strconv.FormatInt(id, 10) // ours, not Immich's id, so it is always a safe file name
	tmp, err := os.CreateTemp(a.photoDir(), name+".*.tmp")
	if err != nil {
		return "", err
	}
	n, err := io.Copy(tmp, io.LimitReader(res.Body, 50<<20)) // cut, as before: past the client's limit the copy would fail on every prefetch
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), filepath.Join(a.photoDir(), name))
	}
	if err != nil {
		os.Remove(tmp.Name())
		return "", transient(err)
	}
	if _, err := a.DB.ExecContext(ctx, "UPDATE photos_cache SET cache_path = ?, cache_bytes = ?, last_used_at = ? WHERE id = ?", name, n, now, id); err != nil {
		return "", err
	}
	return filepath.Join(a.photoDir(), name), a.evictPhotos(ctx, id)
}

// evictPhotos drops the least recently used cached bytes, never keep's,
// until the cache fits photoCacheLimit.
func (a *App) evictPhotos(ctx context.Context, keep int64) error {
	for {
		var total int64
		if err := a.DB.QueryRowContext(ctx, "SELECT coalesce(sum(cache_bytes), 0) FROM photos_cache WHERE cache_path IS NOT NULL").Scan(&total); err != nil {
			return err
		}
		if total <= photoCacheLimit {
			return nil
		}
		var id int64
		var path string
		err := a.DB.QueryRowContext(ctx, "SELECT id, cache_path FROM photos_cache WHERE cache_path IS NOT NULL AND id != ? ORDER BY last_used_at, id LIMIT 1", keep).Scan(&id, &path)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if _, err := a.DB.ExecContext(ctx, "UPDATE photos_cache SET cache_path = NULL, cache_bytes = 0 WHERE id = ?", id); err != nil {
			return err
		}
		a.removeCached([]string{path})
	}
}

// getPhoto serves a screensaver photo's bytes: Immich's proxied through the
// cache, so the browser never sees Immich or its key; the Fallback folder's as-is.
func (a *App) getPhoto(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if rel, err := a.folderPhoto(r.Context(), id); err == nil {
		a.serveFolderPhoto(w, r, rel)
		return
	} else if !errors.Is(err, sql.ErrNoRows) {
		a.serverError(w, r, err)
		return
	}
	path, err := a.cachedPhoto(r.Context(), id)
	if errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		a.Log.Warn("fetching a photo from Immich", "photo", id, "err", err)
		http.Error(w, "photo unavailable", http.StatusBadGateway)
		return
	}
	f, err := os.Open(path)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	defer f.Close()
	w.Header().Set("Cache-Control", "private, max-age=86400")
	http.ServeContent(w, r, "", time.Time{}, f) // sniffs the type: Immich sends octet-stream
}
