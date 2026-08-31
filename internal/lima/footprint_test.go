package lima

import "testing"

func TestParseFootprint(t *testing.T) {
	out := "======\ncom.apple.Virtualization.VirtualMachine [42202]: 64-bit    Footprint: 1552 MB (16384 bytes per page)\n    phys_footprint: 1552 MB\n    phys_footprint_peak: 6039 MB\n"
	if got := parseFootprint(out); got != 1552<<20 {
		t.Errorf("got %d", got)
	}
	if got := parseFootprint("    phys_footprint: 2528 KB\n"); got != 2528<<10 {
		t.Errorf("KB: %d", got)
	}
	if parseFootprint("nothing here") != 0 {
		t.Error("absent → 0")
	}
}

func TestInstanceFromOpenFiles(t *testing.T) {
	home := "/Users/x/.corral/lima"
	out := "p42202\nn/dev/null\nn" + home + "/corral-ee07a8/vz-efi\nn" + home + "/corral-ee07a8/serialv.log\nn" + home + "/corral-ee07a8/disk\n"
	if got := instanceFromOpenFiles(out, home); got != "corral-ee07a8" {
		t.Errorf("got %q", got)
	}
	if got := instanceFromOpenFiles("n/Users/x/.lima/default/disk\n", home); got != "" {
		t.Errorf("foreign LIMA_HOME must not match: %q", got)
	}
}
