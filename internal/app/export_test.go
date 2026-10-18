package app

import "time"

// SetHeartbeat shortens the stream heartbeat for a test.
func SetHeartbeat(d time.Duration) (restore func()) {
	old := heartbeat
	heartbeat = d
	return func() { heartbeat = old }
}
