package ui

import "testing"

func TestMilestone(t *testing.T) {
	cases := map[string]string{
		`time="…" level=info msg="Attempting to download the image" arch=aarch64`:       "downloading the Ubuntu image",
		`INFO[0002] Starting the instance "corral-ee07a8" with internal VM driver "vz"`: "booting the VM",
		`INFO[0010] [hostagent] Waiting for the essential requirement 1 of 1: "ssh"`:    "waiting for SSH",
		`INFO[0020] [hostagent] The essential requirement 1 of 1 is satisfied`:          "SSH is up · provisioning",
		`[corral] installing project packages: jq`:                                      "provisioning: installing project packages: jq",
		`  cloning golden-0289f0304c9c (copy-on-write)`:                                 "cloning the golden image",
		`INFO[0040] READY. Run ` + "`limactl shell x`" + ` to open the shell.`:          "ready",
		`time="…" level=fatal msg=unimplemented`:                                        "error: time=\"…\" level=fatal msg=unimplemented",
	}
	for in, want := range cases {
		got, ok := Milestone(in)
		if !ok || got != want {
			t.Errorf("Milestone(%q) = %q,%v want %q", in, got, ok, want)
		}
	}
	for _, noise := range []string{"{\"level\":\"debug\",\"msg\":\"Time sync: drift -98ms within threshold\"}", "  12.3 MiB / 600 MiB [====>    ] 2%", ""} {
		if _, ok := Milestone(noise); ok {
			t.Errorf("noise %q treated as a milestone", noise)
		}
	}
}

// A phase is shown once even when Lima repeats or interleaves its lines.
func TestProgressModelDedupesPhases(t *testing.T) {
	m := newProgressModel("x")
	for _, l := range []string{
		`[hostagent] Waiting for the essential requirement 1 of 2: "ssh"`,
		`[hostagent] The essential requirement 1 of 2 is satisfied`,
		`[hostagent] Waiting for the essential requirement 2 of 2: "user probe"`,
		`[hostagent] The essential requirement 2 of 2 is satisfied`,
		`READY. Run x`,
	} {
		m.Update(logMsg(l))
	}
	if len(m.lines) != 3 {
		t.Fatalf("want 3 phases, got %d: %q", len(m.lines), m.lines)
	}
}
