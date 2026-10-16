// Package buildinfo exposes version metadata injected at link time.
package buildinfo

import "fmt"

// Set with -ldflags "-X github.com/ncecere/open-mcp-gateway/internal/buildinfo.Version=..."
var (
	Version = "dev"
	Commit  = "unknown"
)

func String() string { return fmt.Sprintf("open-mcp-gateway %s (%s)", Version, Commit) }
