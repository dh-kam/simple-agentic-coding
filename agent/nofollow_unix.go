//go:build unix

package agent

import "syscall"

// oNoFollow makes open() fail when the final path component is a symlink,
// which is what stops a symlink swapped in after safePath resolved the path.
const oNoFollow = syscall.O_NOFOLLOW
