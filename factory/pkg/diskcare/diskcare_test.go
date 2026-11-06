package diskcare

import (
	"os"
	"strings"
	"testing"
)

func TestOf(t *testing.T) {
	u, err := Of(os.TempDir())
	if err != nil || u.Size == 0 || u.Free > u.Size || u.At.IsZero() {
		t.Errorf("Of = %+v, %v", u, err)
	}
	if s := (Usage{Used: 38 * GB, Size: 40 * GB, Free: 2 * GB}).String(); !strings.HasPrefix(s, "38.0/40.0 GB used, 2.0 GB free") {
		t.Errorf("String = %q", s)
	}
}
