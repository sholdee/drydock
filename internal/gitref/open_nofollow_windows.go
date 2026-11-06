//go:build windows

package gitref

// openNoFollow is zero on Windows, which has no O_NOFOLLOW. drydock does
// not release Windows builds, and mkdirSnapshotParents still refuses a
// symlinked directory component there.
const openNoFollow = 0
