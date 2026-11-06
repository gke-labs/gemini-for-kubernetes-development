// Package diskcare measures a sandbox's workspace disk (KCC #15081: a 40 Gi
// workspace filled mid-task, and the task hung). The sandbox's daemon
// serves the measurement (GET /v1/disk); what to do about a full disk - a
// cleanup recipe, run by a watch rule on the sandbox - is the overseer's
// configuration, not code.
package diskcare

import (
	"fmt"
	"time"

	"golang.org/x/sys/unix"
)

// GB is a gigabyte, as df counts them.
const GB = 1 << 30

// Usage is a filesystem's space, in bytes.
type Usage struct {
	Used uint64    `json:"used"`
	Size uint64    `json:"size"`
	Free uint64    `json:"free"`
	At   time.Time `json:"at"`
}

// String is Usage as people read it: "33.1/58.8 GB used, 25.7 GB free".
func (u Usage) String() string {
	return fmt.Sprintf("%.1f/%.1f GB used, %.1f GB free", float64(u.Used)/GB, float64(u.Size)/GB, float64(u.Free)/GB)
}

// Of is the space of the filesystem path is on.
func Of(path string) (Usage, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return Usage{}, fmt.Errorf("statfs %s: %w", path, err)
	}
	bs := uint64(st.Bsize)
	size, free := st.Blocks*bs, st.Bavail*bs
	used := size - st.Bfree*bs
	return Usage{Used: used, Size: size, Free: free, At: time.Now().UTC().Truncate(time.Second)}, nil
}
