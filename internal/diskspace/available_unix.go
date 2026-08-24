//go:build unix

package diskspace

import "syscall"

// statfsAvail returns the bytes available to an unprivileged writer on the filesystem
// holding path.
//
// Bavail, not Bfree: the difference is the reserved block pool, which the run cannot
// write into, so counting it would make the estimate optimistic in exactly the
// situation the estimate exists for. The Bsize conversion is what makes one
// expression compile on both supported platforms — it is int64 on linux and uint32 on
// darwin, while Bavail is uint64 on both.
func statfsAvail(path string) (uint64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, err
	}
	return uint64(st.Bsize) * st.Bavail, nil
}
