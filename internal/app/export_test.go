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
