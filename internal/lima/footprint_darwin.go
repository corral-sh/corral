package lima

import (
	"context"
	"os/exec"
	"strings"
)

// HostFootprints returns, per running instance, the physical memory the Mac
// has committed to its VM process (com.apple.Virtualization.VirtualMachine).
// Instances whose process cannot be found are absent from the map. Best
// effort: any tool failure yields an empty map, never an error to the UI.
func (c *Client) HostFootprints(ctx context.Context) map[string]int64 {
	res := map[string]int64{}
	out, err := exec.CommandContext(ctx, "pgrep", "-f", "com.apple.Virtualization.VirtualMachine").Output()
	if err != nil {
		return res
	}
	for _, pid := range strings.Fields(string(out)) {
		open, err := exec.CommandContext(ctx, "lsof", "-p", pid, "-Fn").Output()
		if err != nil {
			continue
		}
		name := instanceFromOpenFiles(string(open), c.LimaHome)
		if name == "" {
			continue
		}
		fp, err := exec.CommandContext(ctx, "footprint", "-p", pid).Output()
		if err != nil {
			continue
		}
		if n := parseFootprint(string(fp)); n > 0 {
			res[name] = n
		}
	}
	return res
}
