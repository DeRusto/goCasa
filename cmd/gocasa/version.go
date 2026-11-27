package main

import "runtime/debug"

// version is stamped by release builds: -ldflags "-X main.version=v1.2.0".
var version string

// appVersion is GoCasa's version, as the mDNS advert's ver= reports it.
func appVersion() string {
	info, _ := debug.ReadBuildInfo()
	return resolveVersion(version, info)
}

// resolveVersion prefers the release stamp, then the module version Go
// records from VCS, then "dev" for a build with neither.
func resolveVersion(stamped string, info *debug.BuildInfo) string {
	if stamped != "" {
		return stamped
	}
	if info != nil && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return "dev"
}
