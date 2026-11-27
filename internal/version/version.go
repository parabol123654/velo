// Package version holds the release version, set at build time:
//
//	go build -ldflags "-X github.com/parabol123654/velo/internal/version.Version=v0.3.0"
//
// The Makefile, Dockerfile and release workflow do this. A plain go build
// reports "dev".
package version

// Version is the release tag, such as "v0.3.0", or "dev".
var Version = "dev"
