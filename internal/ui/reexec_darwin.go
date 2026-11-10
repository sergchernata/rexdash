package ui

/*
#include <spawn.h>
#include <signal.h>
#include <stdlib.h>

extern char **environ;

// Replace this process with path, like execve, using posix_spawn's SETEXEC mode, with a
// clean signal mask and default handlers for the new image. Returns an errno on failure.
static int spawn_exec(const char *path, char *const argv[]) {
	posix_spawnattr_t attr;
	sigset_t none, all;
	int r = posix_spawnattr_init(&attr);
	if (r != 0) return r;
	sigemptyset(&none);
	sigfillset(&all);
	posix_spawnattr_setsigmask(&attr, &none);
	posix_spawnattr_setsigdefault(&attr, &all);
	posix_spawnattr_setflags(&attr, POSIX_SPAWN_SETEXEC | POSIX_SPAWN_SETSIGMASK | POSIX_SPAWN_SETSIGDEF);
	r = posix_spawn(NULL, path, NULL, &attr, argv, environ);
	posix_spawnattr_destroy(&attr);
	return r;
}
*/
import "C"

import (
	"syscall"
	"unsafe"
)

// Reexec replaces this process with exe (same pid, same terminal, same arguments).
//
// Not syscall.Exec: on macOS it first waits for every pending async-preemption signal to be
// delivered (Go issue #41702), and FSEvents runs callbacks on dispatch threads that block
// signals, so that wait can spin forever — panes went blank at ~35% CPU after an upgrade.
func Reexec(exe string, args []string) error {
	cpath := C.CString(exe)
	defer C.free(unsafe.Pointer(cpath))
	argv := make([]*C.char, len(args)+1)
	for i, a := range args {
		argv[i] = C.CString(a)
		defer C.free(unsafe.Pointer(argv[i]))
	}
	if r := C.spawn_exec(cpath, (**C.char)(unsafe.Pointer(&argv[0]))); r != 0 {
		return syscall.Errno(r)
	}
	return nil
}
