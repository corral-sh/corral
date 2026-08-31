package lima

import (
	"bufio"
	"path/filepath"
	"strconv"
	"strings"
)

// The guest's own /proc/meminfo understates what a box costs the Mac: the vz
// guest has no balloon device, so pages the guest freed stay resident in the
// Virtualization.framework process until the box stops (a tester measured
// 485 MiB "used" in the guest against a 6 GB host footprint). The right
// host metric is phys_footprint — `ps rss` double-counts shared and
// reclaimable pages for a VM and reads ~2× too high.

// parseFootprint extracts phys_footprint in bytes from `footprint -p <pid>`
// output; 0 when absent.
func parseFootprint(out string) int64 {
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) >= 3 && f[0] == "phys_footprint:" {
			n, err := strconv.ParseFloat(f[1], 64)
			if err != nil {
				return 0
			}
			switch strings.ToUpper(f[2]) {
			case "B", "BYTES":
				return int64(n)
			case "KB":
				return int64(n * 1024)
			case "MB":
				return int64(n * 1024 * 1024)
			case "GB":
				return int64(n * 1024 * 1024 * 1024)
			}
		}
	}
	return 0
}

// instanceFromOpenFiles maps a VM process to its Lima instance: the
// Virtualization.framework XPC service is not a child of the host agent, so
// the only link is the instance's disk image it holds open. out is
// `lsof -p <pid> -Fn` output (one "n<path>" line per file).
func instanceFromOpenFiles(out, limaHome string) string {
	prefix := filepath.Clean(limaHome) + string(filepath.Separator)
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		l := sc.Text()
		if !strings.HasPrefix(l, "n"+prefix) {
			continue
		}
		rel := strings.TrimPrefix(l[1:], prefix)
		name, file, ok := strings.Cut(rel, string(filepath.Separator))
		if ok && (file == "disk" || file == "diffdisk" || file == "basedisk") {
			return name
		}
	}
	return ""
}
