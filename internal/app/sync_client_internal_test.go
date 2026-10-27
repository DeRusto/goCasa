package app

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// answering is a server that answers every request with status and body.
func answering(t *testing.T, status int, body string) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func testClient(overrides map[int]error) syncClient {
	return syncClient{name: "Thing", client: http.DefaultClient, limit: 1 << 10, overrides: overrides}
}

func TestSyncClientRule(t *testing.T) {
	for _, tc := range []struct {
		status    int
		body      string
		transient bool
		msg       string // "" is no error
	}{
		{200, `{"a":1}`, false, ""},
		{500, "", true, "Thing answered 500 Internal Server Error"},
		{503, "", true, "Thing answered 503 Service Unavailable"},
		{429, "", true, "Thing answered 429 Too Many Requests"},
		{401, "", false, "Thing refused the credentials"},
		{403, "", false, "Thing refused the credentials"},
		{404, "", false, "Thing answered 404 Not Found"},
		{400, "", false, "Thing answered 400 Bad Request"},
		{200, "<html>", false, "Thing's answer isn't JSON"},
		{200, `{"a":`, false, "Thing's answer isn't JSON"}, // whole, but cut short by the sender
		{200, `{"a":1}<html>`, false, "Thing's answer isn't JSON"},
		{200, `{"a":1}` + "\n", false, ""}, // trailing whitespace is fine
	} {
		srv := answering(t, tc.status, tc.body)
		var out struct{ A int }
		err := testClient(nil).json(context.Background(), http.MethodGet, srv.URL, nil, &out)
		if tc.msg == "" {
			if err != nil || out.A != 1 {
				t.Errorf("%d %q: err %v, out %+v", tc.status, tc.body, err, out)
			}
			continue
		}
		if err == nil || !strings.HasPrefix(err.Error(), tc.msg) {
			t.Errorf("%d %q: err %v, want %q", tc.status, tc.body, err, tc.msg)
		}
		if got := errors.As(err, new(transientError)); got != tc.transient {
			t.Errorf("%d %q: transient = %v", tc.status, tc.body, got)
		}
	}
}

func TestSyncClientNetworkErrorIsTransientWithoutTheURL(t *testing.T) {
	srv := answering(t, 200, "{}")
	srv.Close()
	err := testClient(nil).json(context.Background(), http.MethodGet, srv.URL+"/feed?token=s3cret", nil, new(any))
	if !errors.As(err, new(transientError)) {
		t.Fatalf("err %v, want transient", err)
	}
	if strings.Contains(err.Error(), "s3cret") {
		t.Errorf("the secret is in the error: %v", err)
	}
	// An address that won't parse leaves its secret out too.
	if _, err := testClient(nil).do(context.Background(), http.MethodGet, "http://[::1/feed?token=s3cret", nil); err == nil || strings.Contains(err.Error(), "s3cret") {
		t.Errorf("bad address: %v", err)
	}
}

func TestSyncClientAnswerCutOffIsTransient(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "100")
		io.WriteString(w, `{"a":1,`) // then the connection closes
	}))
	defer srv.Close()
	err := testClient(nil).json(context.Background(), http.MethodGet, srv.URL, nil, new(any))
	if !errors.As(err, new(transientError)) {
		t.Errorf("err %v, want transient", err)
	}
}

func TestSyncClientTimeoutIsTransient(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-release }))
	defer srv.Close()
	defer close(release)
	c := testClient(nil)
	c.client = &http.Client{Timeout: 50 * time.Millisecond}
	if err := c.json(context.Background(), http.MethodGet, srv.URL, nil, new(any)); !errors.As(err, new(transientError)) {
		t.Errorf("err %v, want transient", err)
	}
}

func TestSyncClientOverridesWin(t *testing.T) {
	gone := errors.New("gone")
	c := testClient(map[int]error{404: gone, 503: gone, 304: nil})
	for _, status := range []int{404, 503} {
		if err := c.json(context.Background(), http.MethodGet, answering(t, status, "").URL, nil, new(any)); err != gone {
			t.Errorf("%d: err %v, want the override", status, err)
		}
	}
	res, err := c.do(context.Background(), http.MethodGet, answering(t, 304, "").URL, nil)
	if err != nil || res.StatusCode != 304 {
		t.Fatalf("a nil override: %v, %v", res, err)
	}
	res.Body.Close()
	if err := c.json(context.Background(), http.MethodGet, answering(t, 304, "").URL, nil, new(any)); err != nil {
		t.Errorf("a nil override decoded: %v", err)
	}
}

func TestSyncClientBodyLimit(t *testing.T) {
	big := `"` + strings.Repeat("x", 2<<10) + `"`
	err := testClient(nil).json(context.Background(), http.MethodGet, answering(t, 200, big).URL, nil, new(string))
	if err == nil || errors.As(err, new(transientError)) {
		t.Errorf("an answer over the limit: %v, want permanent", err)
	}
	res, err := testClient(nil).do(context.Background(), http.MethodGet, answering(t, 200, big).URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if _, err := io.ReadAll(res.Body); err == nil {
		t.Error("do read past the limit")
	}
}

func TestSyncClientSendsHeadersAndJSON(t *testing.T) {
	var got http.Header
	var sent string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header
		b, _ := io.ReadAll(r.Body)
		sent = string(b)
	}))
	defer srv.Close()
	c := testClient(nil)
	c.header = http.Header{"X-Api-Key": {"k"}}
	if err := c.json(context.Background(), http.MethodPost, srv.URL, map[string]int{"a": 1}, nil); err != nil {
		t.Fatal(err)
	}
	if got.Get("X-Api-Key") != "k" || got.Get("Content-Type") != "application/json" || sent != `{"a":1}` {
		t.Errorf("headers %v, body %q", got, sent)
	}
}
