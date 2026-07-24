//go:build linux

// Package namespaces holds the isolation primitive: launching a process into
// fresh Linux namespaces, and adjusting the namespace-scoped state (the
// hostname) once inside.
package namespaces

import (
	"os/exec"
	"syscall"
)

// cloneFlags are the namespaces the contained process is born into. Each flag
// gives the child a private copy of one global kernel resource:
//
//	CLONE_NEWUTS -- hostname + domainname, so SetHostname below can't touch the host's
//	CLONE_NEWPID -- PID tree, so the child comes up as PID 1 and can't see host PIDs
//	CLONE_NEWNS  -- mount table, so rootfs.PivotInto's mounts stay inside the container
//	CLONE_NEWIPC -- System V IPC + POSIX message queues
//
// Deliberately absent: CLONE_NEWNET (the container shares the host network) and
// CLONE_NEWUSER (no rootless / UID mapping). See the README's non-goals.
const cloneFlags = syscall.CLONE_NEWUTS |
	syscall.CLONE_NEWPID |
	syscall.CLONE_NEWNS |
	syscall.CLONE_NEWIPC

// Command builds an *exec.Cmd that re-execs the current program
// (/proc/self/exe) with the given args, set up to run in new namespaces. The
// caller (cmd/minirun) wires stdio and Start()s it; this function's whole job is
// to attach the right SysProcAttr so the child lands in fresh namespaces.
//
// Re-execing /proc/self/exe -- the kernel's symlink to our own binary -- is how
// we get a second copy of minirun running as the child. args starts with
// "__child" so main's dispatch routes it to runChild.
//
// Why a separate child process at all: entering a new PID namespace only affects
// FUTURE children, never the calling process -- so we can't unshare(CLONE_NEWPID)
// in place; we have to spawn a child with the flag set. That child is PID 1 of
// the new namespace. This is the single most important structural fact about the
// whole runtime.
func Command(args []string) *exec.Cmd {
	cmd := exec.Command("/proc/self/exe", args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags: cloneFlags,
		// Reap the child if the parent dies, rather than leaving a container
		// process orphaned onto the host's init with a cgroup nobody cleans up.
		Pdeathsig: syscall.SIGKILL,
	}
	return cmd
}

// SetHostname sets the hostname. Because the child runs in its own UTS namespace
// (CLONE_NEWUTS above), this changes only the container's hostname, not the
// host's.
func SetHostname(name string) error {
	return syscall.Sethostname([]byte(name))
}
