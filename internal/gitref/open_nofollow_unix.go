//go:build !windows

package gitref

import "syscall"

// openNoFollow makes os.OpenFile fail on a symlink at the final path
// component instead of following it.
const openNoFollow = syscall.O_NOFOLLOW
