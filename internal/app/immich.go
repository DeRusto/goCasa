package app

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/DeRusto/goCasa/internal/web"
	"github.com/a-h/templ"
)

// immich lists the photos of the albums and people an Admin picked (spec
// §10), and today's Memories if picked, into photos_cache for the
// screensaver. Its credentials are the API key, its config_json the address,
// what can be picked and what is.
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
	Memories     bool          `json:"memories"` // today's "on this day" photos join the picks
	// A photo in an excluded album or showing an excluded person never shows.
	ExcludedAlbums []string `json:"excluded_albums"`
	ExcludedPeople []string `json:"excluded_people"`
	Favorites      bool     `json:"favorites_only"`
	MinRating      int      `json:"min_rating"`    // stars, 0 for any
	MemoriesNote   string   `json:"memories_note"` // why Memories show nothing, from the last sync
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
	refused := errors.New("Immich refused the API key: it needs asset.read, asset.view, album.read, person.read and memory.read")
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
		if assets, err = c.search(ctx, cfg); err != nil {
			return err
		}
	}
	var note string
	if cfg.Memories {
		mem, err := c.memories(ctx, a.Now())
		switch {
		case errors.As(err, new(transientError)):
			return err
		case err != nil: // a key without memory.read: the other picks still show
			note = "Memories show nothing: Immich refused the API key's memory.read permission. Make a new key with it, then disconnect and connect again."
		}
		assets = append(assets, mem...)
	}
	if _, err := a.DB.ExecContext(ctx, "UPDATE integrations SET config_json = json_set(config_json, '$.memories_note', ?) WHERE id = ?", note, in.id); err != nil {
		return err
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

// immichAsset is a photo as Immich lists it; the Fallback folder fills in
// ID, TakenAt and the size.
type immichAsset struct {
	ID            string    `json:"id"`
	TakenAt       time.Time `json:"fileCreatedAt"`
	Local         time.Time `json:"localDateTime"` // when taken, in the photographer's zone, written as UTC
	Width, Height int       // upright, else 0
	Type          string    `json:"type"`
	Visibility    string    `json:"visibility"`
	Trashed       bool      `json:"isTrashed"`
	Exif          struct {
		City, State, Country string          // json matches these case-blind
		Width                int             `json:"exifImageWidth"`
		Height               int             `json:"exifImageHeight"`
		Orientation          json.RawMessage `json:"orientation"` // "6", or a number in some versions
	} `json:"exifInfo"`
}

// taken is when as was taken, for its caption.
func (as immichAsset) taken() time.Time {
	if !as.Local.IsZero() {
		return as.Local
	}
	return as.TakenAt
}

// size is as's upright width and height: Immich's own, else its EXIF size
// turned by its orientation; 0 when unknown.
func (as immichAsset) size() (int, int) {
	if as.Width > 0 && as.Height > 0 {
		return as.Width, as.Height
	}
	switch strings.Trim(string(as.Exif.Orientation), `"`) {
	case "5", "6", "7", "8": // turned a quarter
		return as.Exif.Height, as.Exif.Width
	}
	return as.Exif.Width, as.Exif.Height
}

// place is where as was taken, e.g. "Lisbon, Portugal"; "" when unknown.
func (as immichAsset) place() string {
	var p []string
	for _, s := range []string{cmp.Or(as.Exif.City, as.Exif.State), as.Exif.Country} {
		if s != "" {
			p = append(p, s)
		}
	}
	return strings.Join(p, ", ")
}

// search lists the photos in any of the picked albums or showing any of the
// picked people, but in no excluded album and showing no excluded person,
// only favorites or rated at least the minimum if set,
// leaving out trashed, archived, hidden and non-image assets: v3.2's filter
// shape excludes none of them by default.
func (c immichConn) search(ctx context.Context, cfg immichConfig) ([]immichAsset, error) {
	var or []any
	if len(cfg.PickedAlbums) > 0 {
		or = append(or, map[string]any{"albumIds": map[string]any{"any": cfg.PickedAlbums}})
	}
	if len(cfg.PickedPeople) > 0 {
		or = append(or, map[string]any{"personIds": map[string]any{"any": cfg.PickedPeople}})
	}
	filter := map[string]any{
		"or":         or,
		"trashedAt":  map[string]any{"eq": nil},
		"visibility": map[string]any{"eq": "timeline"},
		"type":       map[string]any{"eq": "IMAGE"},
	}
	if len(cfg.ExcludedAlbums) > 0 { // top-level filters AND with the or
		filter["albumIds"] = map[string]any{"none": cfg.ExcludedAlbums}
	}
	if len(cfg.ExcludedPeople) > 0 {
		filter["personIds"] = map[string]any{"none": cfg.ExcludedPeople}
	}
	if cfg.Favorites {
		filter["isFavorite"] = map[string]any{"eq": true}
	}
	if cfg.MinRating > 0 { // unrated photos drop out
		filter["rating"] = map[string]any{"gte": cfg.MinRating}
	}
	body := map[string]any{"filter": filter, "size": 1000, "withExif": true}
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

// memories is the photos of today's Immich Memories ("on this day").
func (c immichConn) memories(ctx context.Context, now time.Time) ([]immichAsset, error) {
	var ms []struct {
		Assets []immichAsset `json:"assets"`
	}
	if err := c.call(ctx, http.MethodGet, "/api/memories?for="+url.QueryEscape(now.UTC().Format(time.RFC3339)), nil, &ms); err != nil {
		return nil, err
	}
	var out []immichAsset
	for _, m := range ms {
		for _, as := range m.Assets {
			if as.Type == "IMAGE" && !as.Trashed && (as.Visibility == "" || as.Visibility == "timeline") {
				out = append(out, as)
			}
		}
	}
	return out, nil
}

// replacePhotos makes source's rotation list exactly assets, keeping the
// cached bytes of those still in it.
func (a *App) replacePhotos(ctx context.Context, source string, assets []immichAsset) error {
	var gone []string
	err := a.inTx(ctx, func(tx *sql.Tx) error {
		keep := map[string]bool{}
		for _, as := range assets {
			keep[as.ID] = true
			w, h := as.size()
			if _, err := tx.ExecContext(ctx, `INSERT INTO photos_cache (source, external_id_or_path, taken_at, width, height, place) VALUES (?, ?, ?, ?, ?, ?)
				ON CONFLICT (source, external_id_or_path) DO UPDATE SET taken_at = excluded.taken_at, width = excluded.width, height = excluded.height, place = excluded.place`,
				source, as.ID, as.taken().Unix(), w, h, as.place()); err != nil {
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
			if _, err := c.memories(ctx, a.Now()); err != nil {
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

// saveImmichPicks sets the picked and excluded albums and people, Memories
// and the favorites and rating filters, then re-lists.
func (a *App) saveImmichPicks(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	// Each album and person is "album:<id>" or "person:<id>", set to show, hide or neither.
	picks := map[string][]string{}
	for k, v := range r.PostForm {
		kind, id, ok := strings.Cut(k, ":")
		if !ok || (kind != "album" && kind != "person") || id == "" {
			continue
		}
		switch v[0] {
		case "show":
			picks[kind] = append(picks[kind], id)
		case "hide":
			picks["exclude_"+kind] = append(picks["exclude_"+kind], id)
		case "":
		default:
			a.settingsPage(w, r, http.StatusBadRequest, "Each album and person is Show, Hide or neither.")
			return
		}
	}
	ids := func(name string) string {
		b, _ := json.Marshal(append([]string{}, slices.Sorted(slices.Values(picks[name]))...))
		return string(b)
	}
	rating, err := strconv.Atoi(cmp.Or(r.PostFormValue("min_rating"), "0"))
	if err != nil || rating < 0 || rating > 5 {
		a.settingsPage(w, r, http.StatusBadRequest, "The minimum rating is 0 to 5 stars.")
		return
	}
	if _, err := a.exec(r.Context(), web.KindIntegration, `UPDATE integrations SET config_json = json_set(config_json, '$.picked_albums', json(?), '$.picked_people', json(?),
		'$.excluded_albums', json(?), '$.excluded_people', json(?), '$.memories', json(?), '$.favorites_only', json(?), '$.min_rating', ?) WHERE type = ?`,
		ids("album"), ids("person"), ids("exclude_album"), ids("exclude_person"),
		strconv.FormatBool(r.PostForm.Has("memories")), strconv.FormatBool(r.PostForm.Has("favorites")), rating, immichType); err != nil {
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
	picked, excluded := map[string]bool{}, map[string]bool{}
	for _, id := range append(cfg.PickedAlbums, cfg.PickedPeople...) {
		picked[id] = true
	}
	for _, id := range append(cfg.ExcludedAlbums, cfg.ExcludedPeople...) {
		excluded[id] = true
	}
	for _, x := range cfg.Albums {
		m.Albums = append(m.Albums, web.ImmichPick{ID: x.ID, Name: x.Name, Picked: picked[x.ID], Excluded: excluded[x.ID]})
	}
	for _, x := range cfg.People {
		m.People = append(m.People, web.ImmichPick{ID: x.ID, Name: x.Name, Picked: picked[x.ID], Excluded: excluded[x.ID]})
	}
	m.NothingPicked = len(picked) == 0 && !cfg.Memories
	m.Memories, m.MemoriesNote, m.Favorites, m.MinRating = cfg.Memories, cfg.MemoriesNote, cfg.Favorites, cfg.MinRating
	err = a.DB.QueryRowContext(ctx, "SELECT count(*) FROM photos_cache WHERE source = 'immich'").Scan(&m.Photos)
	return &m, err
}

// prefetchPhotos caches the next photos the screensaver will show.
func (a *App) prefetchPhotos(ctx context.Context, _ time.Time) error {
	for _, id := range a.upcoming(immichPrefetch) {
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
