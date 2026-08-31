//go:build !darwin

package lima

import "context"

// HostFootprints is macOS-only (footprint(1), Virtualization.framework).
func (c *Client) HostFootprints(context.Context) map[string]int64 { return map[string]int64{} }
