package app_test

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DeRusto/goCasa/internal/app"
	"github.com/DeRusto/goCasa/internal/apptest"
)

// fakeGoogle plays Google's consent page, token, calendar list and revoke endpoints.
type fakeGoogle struct {
	mu        sync.Mutex
	login     string            // the sub of whoever signs in on the consent page
	calendars []map[string]any  // what the calendar list returns
	consents  []url.Values      // each consent page request
	access    map[string]string // access token → sub
	revoked   []string
	issued    int
	events    map[string][]map[string]any // calendar id → what a full list returns
	changes   map[string][]map[string]any // calendar id → what the next token sync returns
	gone      bool                        // token syncs answer 410
	withdrawn bool                        // refreshes answer invalid_grant
	fail      int                         // when set, events lists answer it
	missing   string                      // a calendar whose events list answers 404
	pageSize  int
	noZone    bool                      // events lists send no calendar timeZone
	lists     []url.Values              // each events list's query, with its calendar as "cal"
	writes    []gWrite                  // each request to one event, and each insert
	got       map[string]map[string]any // event id → what a GET of it answers
	etags     map[string]string         // event id → its etag now: an If-Match of another answers 412, none 428
	writeFail int                       // when set, writes answer it
	failOn    string                    // the methods writeFail applies to, e.g. "PATCH DELETE"; "" is every write
	*httptest.Server
}

func newGoogle(t *testing.T) *fakeGoogle {
	g := &fakeGoogle{login: "sub-alice", access: map[string]string{}, events: map[string][]map[string]any{}, changes: map[string][]map[string]any{}, pageSize: 2,
		got: map[string]map[string]any{}, etags: map[string]string{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /o/oauth2/v2/auth", func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		defer g.mu.Unlock()
		q := r.URL.Query()
		g.consents = append(g.consents, q)
		back := q.Get("redirect_uri") + "?" + url.Values{"code": {"code-" + g.login}, "state": {q.Get("state")}}.Encode()
		http.Redirect(w, r, back, http.StatusFound)
	})
	mux.HandleFunc("POST /token", func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		defer g.mu.Unlock()
		if r.PostFormValue("grant_type") == "refresh_token" {
			tok := strings.TrimPrefix(r.PostFormValue("refresh_token"), "refresh-")
			i := strings.LastIndex(tok, "-")
			if i < 0 || g.withdrawn {
				w.WriteHeader(http.StatusBadRequest)
				w.Write([]byte(`{"error":"invalid_grant"}`))
				return
			}
			g.issued++
			access := fmt.Sprintf("access-%d", g.issued)
			g.access[access] = tok[:i]
			json.NewEncoder(w).Encode(map[string]string{"access_token": access})
			return
		}
		sub, ok := strings.CutPrefix(r.PostFormValue("code"), "code-")
		if !ok || r.PostFormValue("client_id") != "client-id" || r.PostFormValue("client_secret") != "client-secret" || r.PostFormValue("grant_type") != "authorization_code" {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"error":"invalid_grant"}`))
			return
		}
		g.issued++
		access := fmt.Sprintf("access-%d", g.issued)
		g.access[access] = sub
		claims, _ := json.Marshal(map[string]any{"sub": sub, "email": strings.TrimPrefix(sub, "sub-") + "@example.com", "aud": "client-id",
			"iss": "https://accounts.google.com", "exp": apptest.Start.Add(24 * time.Hour).Unix()})
		json.NewEncoder(w).Encode(map[string]string{
			"access_token":  access,
			"refresh_token": fmt.Sprintf("refresh-%s-%d", sub, g.issued),
			"id_token":      "e30." + base64.RawURLEncoding.EncodeToString(claims) + ".sig",
		})
	})
	mux.HandleFunc("GET /calendar/v3/users/me/calendarList", func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		defer g.mu.Unlock()
		if _, ok := g.access[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")]; !ok {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"items": g.calendars})
	})
	mux.HandleFunc("GET /calendar/v3/calendars/{cal}/events", func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		defer g.mu.Unlock()
		if _, ok := g.access[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")]; !ok {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		q := r.URL.Query()
		q.Set("cal", r.PathValue("cal"))
		g.lists = append(g.lists, q)
		cal := r.PathValue("cal")
		items := g.events[cal]
		switch {
		case g.fail != 0:
			w.WriteHeader(g.fail)
			return
		case cal == g.missing:
			w.WriteHeader(http.StatusNotFound)
			return
		case q.Has("syncToken") && g.gone:
			w.WriteHeader(http.StatusGone)
			return
		case q.Has("syncToken"):
			items = g.changes[cal]
		}
		from, _ := strconv.Atoi(q.Get("pageToken"))
		to := min(from+g.pageSize, len(items))
		body := map[string]any{"items": items[from:to], "timeZone": "America/New_York"}
		if g.noZone {
			delete(body, "timeZone")
		}
		if to < len(items) {
			body["nextPageToken"] = strconv.Itoa(to)
		} else {
			if q.Has("syncToken") {
				delete(g.changes, cal)
			}
			body["nextSyncToken"] = fmt.Sprintf("token-%d", len(g.lists))
		}
		json.NewEncoder(w).Encode(body)
	})
	event := func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		defer g.mu.Unlock()
		if _, ok := g.access[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")]; !ok {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		id := r.PathValue("id")
		if r.Method == http.MethodGet {
			if e, ok := g.got[id]; ok {
				json.NewEncoder(w).Encode(e)
			} else {
				w.WriteHeader(http.StatusNotFound)
			}
			return
		}
		wr := gWrite{method: r.Method, cal: r.PathValue("cal"), id: id, ifMatch: r.Header.Get("If-Match")}
		json.NewDecoder(r.Body).Decode(&wr.body)
		g.writes = append(g.writes, wr)
		switch {
		case g.writeFail != 0 && (g.failOn == "" || strings.Contains(g.failOn, r.Method)):
			w.WriteHeader(g.writeFail)
		case r.Method != http.MethodPost && wr.ifMatch == "" && g.etags[id] != "" && !strings.HasPrefix(id, "new"):
			// GoCasa's own new Series, taken back after a failed Split, is the one write without a version.
			w.WriteHeader(http.StatusPreconditionRequired)
		case wr.ifMatch != "" && g.etags[id] != "" && wr.ifMatch != g.etags[id]:
			w.WriteHeader(http.StatusPreconditionFailed)
			w.Write([]byte(`{"error":{"code":412,"errors":[{"reason":"conditionNotMet"}]}}`))
		case r.Method == http.MethodPost:
			json.NewEncoder(w).Encode(map[string]any{"id": fmt.Sprintf("new%d", len(g.writes))})
		case r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		default:
			json.NewEncoder(w).Encode(map[string]any{"id": id})
		}
	}
	mux.HandleFunc("/calendar/v3/calendars/{cal}/events/{id}", event)
	mux.HandleFunc("POST /calendar/v3/calendars/{cal}/events", event)
	mux.HandleFunc("POST /revoke", func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		defer g.mu.Unlock()
		g.revoked = append(g.revoked, r.PostFormValue("token"))
	})
	g.Server = httptest.NewServer(mux)
	t.Cleanup(g.Close)
	return g
}

// gWrite is a write to Google's events API, as the fake got it.
type gWrite struct {
	method, cal, id, ifMatch string
	body                     map[string]any
}

// sent is the writes Google got, oldest first.
func (g *fakeGoogle) sent() []gWrite {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]gWrite(nil), g.writes...)
}

func (g *fakeGoogle) signInAs(sub string) { g.mu.Lock(); g.login = sub; g.mu.Unlock() }
func (g *fakeGoogle) list(cals ...map[string]any) {
	g.mu.Lock()
	g.calendars = cals
	g.mu.Unlock()
}
func (g *fakeGoogle) tokens() int { g.mu.Lock(); defer g.mu.Unlock(); return g.issued }
func (g *fakeGoogle) revocations() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.revoked...)
}

func cal(id, summary, color, role string) map[string]any {
	return map[string]any{"id": id, "summary": summary, "backgroundColor": color, "accessRole": role}
}

// googleHousehold is household with Google set up against g.
func googleHousehold(t *testing.T, g *fakeGoogle, opts ...apptest.Option) *apptest.Harness {
	t.Helper()
	h := apptest.New(t, append([]apptest.Option{func(o *apptest.Setup) {
		o.Upstreams = app.Upstreams{GoogleAuth: g.URL, GoogleAPI: g.URL, GoogleOAuth: g.URL}
		o.Config.GoogleClientID, o.Config.GoogleClientSecret = "client-id", "client-secret"
	}}, opts...)...)
	h.App.Config.GoogleRedirectURI = h.Server.URL + "/auth/google/callback" // known only once serving
	setup(t, h)
	h.Post(t, "/settings/members", member("Bob", "#22c55e", "member", ""))
	return h
}

func storedToken(t *testing.T, h *apptest.Harness, id int64) string {
	t.Helper()
	var enc []byte
	if err := h.App.DB.QueryRow("SELECT credentials_enc FROM integrations WHERE id = ?", id).Scan(&enc); err != nil {
		t.Fatal(err)
	}
	return string(enc)
}

func TestGoogleNeedsItsOwnClient(t *testing.T) {
	t.Parallel()
	h := household(t)
	if _, page := h.Get(t, "/settings"); !strings.Contains(page, "GOOGLE_CLIENT_ID") || !strings.Contains(page, "http://gocasa.test/auth/google/callback") {
		t.Fatalf("Settings doesn't explain Google setup:\n%s", page)
	}
	if code, _ := h.Post(t, "/integrations/google/connect", nil); code != http.StatusBadRequest {
		t.Fatalf("connect without a client = %d", code)
	}
}

func TestConnectReconnectDisconnectGoogle(t *testing.T) {
	t.Parallel()
	g := newGoogle(t)
	g.list(cal("alice@example.com", "Alice", "#9FC6E7", "owner"), cal("en.usa#holiday", "Holidays", "bad", "reader"))
	h := googleHousehold(t, g)

	code, page := h.Post(t, "/integrations/google/connect", nil)
	if code != http.StatusOK || !strings.Contains(page, "alice@example.com") || !strings.Contains(page, "Holidays") || !strings.Contains(page, "(read-only)") {
		t.Fatalf("after connecting = %d:\n%s", code, page)
	}
	consent := g.consents[0]
	for _, scope := range []string{"openid", "email", "https://www.googleapis.com/auth/calendar.events", "https://www.googleapis.com/auth/calendar.calendarlist.readonly"} {
		if !strings.Contains(" "+consent.Get("scope")+" ", " "+scope+" ") {
			t.Fatalf("scope %q lacks %s", consent.Get("scope"), scope)
		}
	}
	if consent.Get("access_type") != "offline" || consent.Get("redirect_uri") != h.Server.URL+"/auth/google/callback" {
		t.Fatalf("consent request = %v", consent)
	}
	var acct int64
	var sub string
	if err := h.App.DB.QueryRow("SELECT id, account_sub FROM integrations WHERE type = 'google_calendar'").Scan(&acct, &sub); err != nil || sub != "sub-alice" {
		t.Fatalf("account = %d %q %v", acct, sub, err)
	}
	if tok := storedToken(t, h, acct); strings.Contains(tok, "refresh-") {
		t.Fatal("token stored in plaintext")
	}
	var aliceCal, enabled int64
	var color string
	h.App.DB.QueryRow("SELECT id, enabled, color FROM source_calendars WHERE external_id = 'alice@example.com'").Scan(&aliceCal, &enabled, &color)
	if enabled != 0 || color != "#9fc6e7" {
		t.Fatalf("new calendar enabled %d colour %q", enabled, color)
	}

	// Show Alice's calendar, with Bob as its default Member.
	if code, _ := h.Post(t, fmt.Sprintf("/settings/google-calendars/%d", aliceCal), url.Values{"enabled": {"on"}, "member": {"2"}}); code != http.StatusOK {
		t.Fatalf("saving the calendar = %d", code)
	}
	h.App.DB.Exec("UPDATE integrations SET sync_cursor = 'cursor-1' WHERE id = ?", acct)
	h.App.DB.Exec("INSERT INTO events (calendar_id, external_id, title, all_day, all_day_date, all_day_end_date, updated_at) VALUES (?, 'ev1', 'Dentist', 1, '2026-03-16', '2026-03-17', 0)", aliceCal)

	// A different login is refused, and its token handed back.
	before := storedToken(t, h, acct)
	g.signInAs("sub-mallory")
	code, page = h.Post(t, "/integrations/google/connect", url.Values{"account": {fmt.Sprint(acct)}})
	if code != http.StatusConflict || !strings.Contains(page, "different Google account") {
		t.Fatalf("reconnecting as someone else = %d:\n%s", code, page)
	}
	if got := g.revocations(); len(got) != 1 || !strings.HasPrefix(got[0], "refresh-sub-mallory-") {
		t.Fatalf("revoked %q", got)
	}
	if storedToken(t, h, acct) != before {
		t.Fatal("the refused login replaced the token")
	}

	// Reconnecting the same login replaces the token and keeps the rest; a new calendar shows up.
	g.signInAs("sub-alice")
	g.list(cal("alice@example.com", "Alice renamed", "#000000", "reader"), cal("work", "Work", "#ffffff", "writer"))
	if code, page := h.Post(t, "/integrations/google/connect", url.Values{"account": {fmt.Sprint(acct)}}); code != http.StatusOK || !strings.Contains(page, "Work") || !strings.Contains(page, "Alice renamed") {
		t.Fatalf("reconnect = %d:\n%s", code, page)
	}
	if last := g.consents[len(g.consents)-1]; last.Get("login_hint") != "alice@example.com" {
		t.Fatalf("reconnect consent = %v", last)
	}
	if storedToken(t, h, acct) == before {
		t.Fatal("reconnect kept the old token")
	}
	var id, members, readOnly int64
	var cursor string
	h.App.DB.QueryRow("SELECT id, enabled, read_only FROM source_calendars WHERE external_id = 'alice@example.com'").Scan(&id, &enabled, &readOnly)
	h.App.DB.QueryRow("SELECT count(*) FROM source_calendar_members WHERE calendar_id = ? AND member_id = 2", id).Scan(&members)
	h.App.DB.QueryRow("SELECT sync_cursor FROM integrations WHERE id = ?", acct).Scan(&cursor)
	if id != aliceCal || enabled != 1 || members != 1 || cursor != "cursor-1" || readOnly != 1 {
		t.Fatalf("after reconnect: calendar %d enabled %d defaults %d cursor %q read-only %d", id, enabled, members, cursor, readOnly)
	}

	// An archived Member isn't on the form, so saving keeps their default for when they're back.
	h.Post(t, "/settings/members/2/archive", nil)
	h.Post(t, fmt.Sprintf("/settings/google-calendars/%d", aliceCal), url.Values{"enabled": {"on"}})
	h.App.DB.QueryRow("SELECT count(*) FROM source_calendar_members WHERE calendar_id = ? AND member_id = 2", aliceCal).Scan(&members)
	if members != 1 {
		t.Fatal("saving the calendar dropped an archived Member's default")
	}

	// Connecting a login that's already here reconnects it rather than adding a second.
	h.Post(t, "/integrations/google/connect", nil)
	var accounts int
	h.App.DB.QueryRow("SELECT count(*) FROM integrations WHERE type = 'google_calendar'").Scan(&accounts)
	if accounts != 1 {
		t.Fatalf("%d Google accounts for one login", accounts)
	}

	// Disconnect needs the warning ticked, then revokes and deletes everything.
	if code, _ := h.Post(t, fmt.Sprintf("/settings/google/%d/delete", acct), nil); code != http.StatusBadRequest {
		t.Fatalf("disconnect without confirming = %d", code)
	}
	if code, _ := h.Post(t, fmt.Sprintf("/settings/google/%d/delete", acct), url.Values{"confirm": {"on"}}); code != http.StatusOK {
		t.Fatalf("disconnect = %d", code)
	}
	if got := g.revocations(); len(got) != 2 || got[1] != fmt.Sprintf("refresh-sub-alice-%d", g.tokens()) { // the token the last reconnect stored
		t.Fatalf("revoked %q", got)
	}
	var left int
	h.App.DB.QueryRow("SELECT (SELECT count(*) FROM integrations WHERE type = 'google_calendar') + (SELECT count(*) FROM source_calendars WHERE integration_id = ?) + (SELECT count(*) FROM source_calendar_members) + (SELECT count(*) FROM events WHERE external_id = 'ev1')", acct).Scan(&left)
	if left != 0 {
		t.Fatalf("%d rows left after disconnect", left)
	}
}

func TestGoogleCallbackChecksState(t *testing.T) {
	t.Parallel()
	g := newGoogle(t)
	h := googleHousehold(t, g)
	if code, _ := h.Get(t, "/auth/google/callback?code=code-sub-alice&state=forged"); code != http.StatusBadRequest {
		t.Fatalf("callback with no state cookie = %d", code)
	}
	var n int
	h.App.DB.QueryRow("SELECT count(*) FROM integrations WHERE type = 'google_calendar'").Scan(&n)
	if n != 0 {
		t.Fatal("a forged callback connected an account")
	}
	// Locked devices can't finish a consent.
	if _, page := h.Device().Get(t, "/auth/google/callback?code=code-sub-alice&state=x"); !strings.Contains(page, "Enter your PIN") {
		t.Fatalf("locked callback:\n%s", page)
	}
}

// Reconnecting one account as another connected login is refused without
// revoking: that would end the other account's grant too.
func TestGoogleRefusalKeepsAnotherAccountsGrant(t *testing.T) {
	t.Parallel()
	g := newGoogle(t)
	h := googleHousehold(t, g)
	h.Post(t, "/integrations/google/connect", nil)
	g.signInAs("sub-bob")
	h.Post(t, "/integrations/google/connect", nil)
	var alice int64
	h.App.DB.QueryRow("SELECT id FROM integrations WHERE account_sub = 'sub-alice'").Scan(&alice)
	if code, _ := h.Post(t, "/integrations/google/connect", url.Values{"account": {fmt.Sprint(alice)}}); code != http.StatusConflict {
		t.Fatalf("reconnecting Alice as Bob = %d", code)
	}
	if got := g.revocations(); len(got) != 0 {
		t.Fatalf("revoked %q", got)
	}

	// Without the client, saved accounts can still be disconnected, not reconnected.
	h.App.Config.GoogleClientID = ""
	_, page := h.Get(t, "/settings")
	if !strings.Contains(page, "Disconnect alice@example.com") || strings.Contains(page, "Reconnect") || strings.Contains(page, "Connect a Google account") {
		t.Fatalf("unconfigured Settings:\n%s", page)
	}
}
