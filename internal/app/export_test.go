package app

import (
	"context"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// SetHeartbeat shortens the stream heartbeat for a test.
func SetHeartbeat(d time.Duration) (restore func()) {
	old := heartbeat
	heartbeat = d
	return func() { heartbeat = old }
}

// SetSyncFault runs f after each event a feed sync writes, inside its transaction.
func SetSyncFault(f func(n int)) (restore func()) {
	old := syncFault
	syncFault = f
	return func() { syncFault = old }
}

// SetPhotoCacheLimit shrinks the photo cache to n bytes for a test.
func SetPhotoCacheLimit(n int) (restore func()) {
	old := photoCacheLimit
	photoCacheLimit = int64(n)
	return func() { photoCacheLimit = old }
}

// ShoppingList is the Shopping list interface, for its adapter tests.
type ShoppingList = shoppingList

// ErrBadPosition is a Move outside the list.
var ErrBadPosition = errBadPosition

// ShoppingList is Shopping list id.
func (a *App) ShoppingList(ctx context.Context, id int64) (ShoppingList, error) {
	return a.shoppingList(ctx, id)
}

// HouseholdReads counts the queries on household_settings so far.
func HouseholdReads(a *App) int64 { return a.householdReads.Load() }

// Cheap PIN hashes: bcrypt's default cost is most of a test's time under -race.
func init() { pinCost = bcrypt.MinCost }
