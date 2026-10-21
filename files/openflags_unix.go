//go:build unix

package files

import "syscall"

// Avoid a blocking open if a regular file is replaced with a named pipe.
const nonBlockingOpenFlag = syscall.O_NONBLOCK
