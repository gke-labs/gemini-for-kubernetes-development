package diskcare

import (
	"os"
	"strings"
	"testing"
)

func TestUsage(t *testing.T) {
	for _, tc := range []struct {
		u         Usage
		low, full bool
	}{
		{Usage{Size: 60 * GB, Free: 30 * GB}, false, false},
		{Usage{Size: 60 * GB, Free: 8 * GB}, true, false},
		{Usage{Size: 40 * GB, Free: 2 * GB}, true, true},
		{Usage{}, false, false}, // not measured
	} {
		if tc.u.Low() != tc.low || tc.u.Full() != tc.full {
			t.Errorf("%+v: low %v full %v", tc.u, tc.u.Low(), tc.u.Full())
		}
	}
	if err := (&ErrFull{Usage{Used: 38 * GB, Size: 40 * GB, Free: 2 * GB}}).Error(); !strings.HasPrefix(err, "disk full: 38.0/40.0 GB used, 2.0 GB free") {
		t.Errorf("ErrFull = %q", err)
	}
	if in := CleanupInputs(Usage{Free: 3}, 7); in["workspace"] != Workspace || in["free_bytes"] != "3" || in["target_bytes"] != "7" {
		t.Errorf("CleanupInputs = %v", in)
	}
}

func TestOf(t *testing.T) {
	u, err := Of(os.TempDir())
	if err != nil || u.Size == 0 || u.Free > u.Size {
		t.Errorf("Of = %+v, %v", u, err)
	}
}
