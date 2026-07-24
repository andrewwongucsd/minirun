//go:build linux

package cgroups

import (
	"os"
	"testing"
)

// TestMemoryLimitOOM is the real-boundary test for the resource-limit primitive
// (milestone 2): it should prove the cgroup's memory.max actually enforces --
// i.e. a process that allocates past the limit gets OOM-killed at the CGROUP
// limit, not the host's total memory.
//
// The skip guards below are complete: this test can only run as root on a
// cgroup v2 host (i.e. inside `make dev-shell` / `make docker-test`, or on a
// Linux box), so it skips cleanly anywhere else instead of failing spuriously.
//
// Milestone 2 -- write the body:
//   - Create a cgroup with a small memory.max (say 32 MB) via New(...).
//   - Start a child process that deliberately allocates more than that (a tiny
//     helper: `sh -c` running a memory hog, or a re-exec of a Go allocator).
//   - AddProcess it to the cgroup BEFORE it allocates (mind the same race the
//     parent handles in main.go).
//   - Assert it dies by SIGKILL / OOM (the kernel kills it) rather than
//     succeeding, and that the host stayed healthy.
//   - Cleanup the cgroup.
//
// Report failures with t.Errorf where you're in a goroutine; t.Fatal is fine on
// the test goroutine itself.
func TestMemoryLimitOOM(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root to create cgroups; run inside `make dev-shell` or on a Linux host as root")
	}
	if _, err := os.Stat("/sys/fs/cgroup/cgroup.controllers"); err != nil {
		t.Skip("cgroup v2 unified hierarchy not mounted at /sys/fs/cgroup; skipping")
	}

	t.Fatal("TODO -- milestone 2: assert OOM-kill fires at the cgroup memory limit")
}
