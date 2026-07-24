//go:build linux

// Package namespaces holds the isolation primitive: launching a process into
// fresh Linux namespaces, and adjusting the namespace-scoped state (the
// hostname) once inside.
package namespaces

import "os/exec"

// Command builds an *exec.Cmd that re-execs the current program
// (/proc/self/exe) with the given args, set up to run in new namespaces. The
// caller (cmd/minirun) wires stdio and Start()s it; this function's whole job is
// to attach the right SysProcAttr so the child lands in fresh namespaces.
//
// Milestone 1 -- implement:
//   - Build exec.Command("/proc/self/exe", args...). Re-execing /proc/self/exe
//     (a symlink to our own binary) is how we get a second copy of minirun
//     running as the child; the args here start with "__child" so main's
//     dispatch routes it correctly.
//   - Set cmd.SysProcAttr = &syscall.SysProcAttr{Cloneflags: ...} OR'ing
//     together the namespaces you want:
//     CLONE_NEWUTS  -- own hostname (so SetHostname below doesn't affect host)
//     CLONE_NEWPID  -- own PID tree (the child sees itself as PID 1)
//     CLONE_NEWNS   -- own mount namespace (so pivot_root doesn't touch host)
//     CLONE_NEWIPC  -- own System V IPC / POSIX message queues
//   - Return the cmd.
//
// Why a separate child process at all: entering a new PID namespace only affects
// FUTURE children, never the calling process -- so we can't unshare(CLONE_NEWPID)
// in place; we have to spawn a child with the flag set. That child is PID 1 of
// the new namespace. This is the single most important structural fact about the
// whole runtime.
func Command(args []string) *exec.Cmd {
	panic("not implemented -- milestone 1: re-exec /proc/self/exe with Cloneflags")
}

// SetHostname sets the hostname. Because the child runs in its own UTS namespace
// (CLONE_NEWUTS above), this changes only the container's hostname, not the
// host's. Milestone 1: a one-liner over syscall.Sethostname([]byte(name)).
func SetHostname(name string) error {
	panic("not implemented -- milestone 1: syscall.Sethostname")
}
