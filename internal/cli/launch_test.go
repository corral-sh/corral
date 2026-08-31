package cli

import "testing"

func TestShouldConfirmCreate(t *testing.T) {
	cases := []struct {
		yes   bool
		plain string
		want  bool
	}{
		{false, "", true},   // interactive first run asks
		{true, "", false},   // --yes
		{false, "1", false}, // CORRAL_PLAIN=1: scripts and make e2e never block on a prompt
		{true, "1", false},
	}
	for _, c := range cases {
		if got := shouldConfirmCreate(c.yes, c.plain); got != c.want {
			t.Errorf("shouldConfirmCreate(%v,%q)=%v want %v", c.yes, c.plain, got, c.want)
		}
	}
}
