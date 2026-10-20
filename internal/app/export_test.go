package app

import "time"

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

// HouseholdReads counts the queries on household_settings so far.
func HouseholdReads(a *App) int64 { return a.householdReads.Load() }
