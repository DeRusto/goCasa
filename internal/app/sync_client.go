package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

// transientError is a failure that retries on the normal schedule, without
// backoff: network trouble, a timeout, a 5xx or a 429 (spec §7.2).
type transientError struct{ error }

// transient marks err as a transientError, without a url.Error's URL.
func transient(err error) error { return transientError{noURL(err)} }

// byStatus marks err transient when an answer of status may pass.
func byStatus(status int, err error) error {
	if status >= 500 || status == http.StatusTooManyRequests {
		return transient(err)
	}
	return err
}

// syncClient is how an Integration's sync talks HTTP: one rule for which
// failures retry on schedule and which back off.
type syncClient struct {
	name      string // as errors say it: "Mealie", "the feed"
	client    *http.Client
	header    http.Header   // sent with every request: auth, conditional GETs
	limit     int64         // bytes of an answer read
	overrides map[int]error // status to its error, winning over the rule; nil is no error
}

// do sends body (if any) as JSON. An answer that's an error is closed; any
// other's body is cut at the limit.
func (c syncClient) do(ctx context.Context, method, u string, body any) (*http.Response, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return nil, fmt.Errorf("%s's address isn't a URL: %w", c.name, noURL(err))
	}
	for k, v := range c.header {
		req.Header[k] = v
	}
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", "GoCasa")
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := c.client.Do(req)
	if err != nil {
		return nil, transient(err)
	}
	if err, ok := c.overrides[res.StatusCode]; ok {
		if err != nil {
			res.Body.Close()
			return nil, err
		}
	} else if res.StatusCode/100 != 2 {
		res.Body.Close()
		if res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden {
			return nil, fmt.Errorf("%s refused the credentials", c.name)
		}
		return nil, byStatus(res.StatusCode, fmt.Errorf("%s answered %s", c.name, res.Status))
	}
	res.Body = http.MaxBytesReader(nil, res.Body, c.limit)
	return res, nil
}

// json is do, then decodes a 2xx answer into out (if any).
func (c syncClient) json(ctx context.Context, method, u string, body, out any) error {
	res, err := c.do(ctx, method, u, body)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if out == nil || res.StatusCode/100 != 2 {
		return nil
	}
	rd := &readFailure{Reader: res.Body}
	if err := json.NewDecoder(rd).Decode(out); err != nil {
		if rd.err != nil && !errors.As(rd.err, new(*http.MaxBytesError)) { // cut off mid-answer, not garbled
			return transient(rd.err)
		}
		return fmt.Errorf("%s's answer isn't JSON: %w", c.name, err)
	}
	return nil
}

// readFailure keeps the first error reading the answer itself, so a
// connection lost mid-answer isn't mistaken for a garbled one.
type readFailure struct {
	io.Reader
	err error
}

func (r *readFailure) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	if err != nil && err != io.EOF && r.err == nil {
		r.err = err
	}
	return n, err
}

// noURL drops a url.Error's URL, which may hold a feed's secret token.
func noURL(err error) error {
	if ue := (*url.Error)(nil); errors.As(err, &ue) {
		return ue.Err
	}
	return err
}
