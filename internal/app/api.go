package app

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
)

// The JSON API for the Display app, versioned from the start: a breaking
// change goes under /api/v2/ while /api/v1/ keeps working.

// apiMember is one Member as GET /api/v1/members gives it.
type apiMember struct {
	ID        int64   `json:"id"`
	Name      string  `json:"name"`
	Color     string  `json:"color"`
	AvatarURL *string `json:"avatarUrl"` // null without an avatar
	Admin     bool    `json:"admin"`
}

// apiMembers lists the active Members who aren't pets. Public: viewing is never locked.
func (a *App) apiMembers(w http.ResponseWriter, r *http.Request) {
	ms, err := a.members(r, false)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	out := []apiMember{}
	for _, m := range ms {
		if m.Archived || m.Pet {
			continue
		}
		am := apiMember{ID: m.ID, Name: m.Name, Color: m.Color, Admin: m.Role == "admin"}
		if m.HasAvatar {
			u := "/members/" + strconv.FormatInt(m.ID, 10) + "/avatar"
			am.AvatarURL = &u
		}
		out = append(out, am)
	}
	writeJSON(w, out)
}

// apiPIN checks an Admin's PIN for the Display app: {"memberId": 1, "pin": "1234"}
// answers {"result": "yes"|"no"|"blocked"}, under the same per-Admin tries and
// block as the PIN pad. It never unlocks: opening native settings must not
// unlock the page behind them.
func (a *App) apiPIN(w http.ResponseWriter, r *http.Request) {
	var body struct {
		MemberID *int64  `json:"memberId"`
		PIN      *string `json:"pin"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil || body.MemberID == nil || body.PIN == nil ||
		!errors.Is(dec.Decode(new(json.RawMessage)), io.EOF) {
		http.Error(w, `the body must be {"memberId": <number>, "pin": "<digits>"}`, http.StatusBadRequest)
		return
	}
	res, err := a.checkPIN(r.Context(), *body.MemberID, *body.PIN)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	writeJSON(w, struct {
		Result pinResult `json:"result"`
	}{res})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(v)
}
