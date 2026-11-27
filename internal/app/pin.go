package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// Wrong PINs (spec §9): pinTries in a row block that Admin's PIN for pinBlock.
const (
	pinTries = 5
	pinBlock = 5 * time.Minute
)

var errPINBlocked = fmt.Sprintf("Too many wrong PINs: that Admin's PIN is blocked for %v minutes.", int(pinBlock.Minutes()))

// pinResult is what checking an Admin's PIN answers.
type pinResult string

const (
	pinYes     pinResult = "yes"     // the PIN matches
	pinNo      pinResult = "no"      // wrong PIN, or not an active Admin
	pinBlocked pinResult = "blocked" // too many wrong tries: blocked for now
)

// checkPIN checks pin against Admin id's, under the per-Admin tries and block
// every PIN check shares (the PIN pad, the JSON API): a wrong try anywhere
// counts towards the same block. It never unlocks the device; callers decide.
func (a *App) checkPIN(ctx context.Context, id int64, pin string) (pinResult, error) {
	now := a.Now().Unix()
	// Count the try before the slow bcrypt check, so parallel guesses can't get
	// past the limit; the try that reaches it starts the block, which ends when
	// pinBlock passes or the PIN is reset. A crash mid-check counts as a wrong try.
	var hash []byte
	var nowBlocked bool
	err := a.DB.QueryRowContext(ctx, `UPDATE members SET
			pin_failures = CASE WHEN pin_failures + 1 >= ?2 THEN 0 ELSE pin_failures + 1 END,
			pin_blocked_until = CASE WHEN pin_failures + 1 >= ?2 THEN ?3 + ?4 ELSE pin_blocked_until END
		WHERE id = ?1 AND role = 'admin' AND NOT archived AND coalesce(pin_blocked_until, 0) <= ?3
		RETURNING pin_hash, coalesce(pin_blocked_until > ?3, 0)`,
		id, pinTries, now, int64(pinBlock/time.Second)).Scan(&hash, &nowBlocked)
	if errors.Is(err, sql.ErrNoRows) {
		err = a.DB.QueryRowContext(ctx, "SELECT 1 FROM members WHERE id = ? AND role = 'admin' AND NOT archived", id).Scan(new(int))
		if err == nil {
			return pinBlocked, nil
		}
		if errors.Is(err, sql.ErrNoRows) {
			return pinNo, nil
		}
	}
	if err != nil {
		return "", err
	}
	if bcrypt.CompareHashAndPassword(hash, []byte(pin)) != nil {
		if nowBlocked {
			return pinBlocked, nil
		}
		return pinNo, nil
	}
	if _, err := a.DB.ExecContext(ctx, "UPDATE members SET pin_failures = 0, pin_blocked_until = NULL WHERE id = ?", id); err != nil {
		return "", err
	}
	return pinYes, nil
}
