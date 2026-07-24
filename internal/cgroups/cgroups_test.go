//go:build linux

package cgroups

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

const (
	// hogEnv carries the allocation size to the helper process below; its
	// presence is also what tells the helper it is the helper.
	hogEnv = "MINIRUN_TEST_ALLOC_MB"

	// limitMB is the cgroup's memory.max. Small enough that the kernel kills the
	// helper quickly, large enough that the Go runtime can start up under it.
	limitMB = 32

	// allocMB is what the helper tries to allocate -- comfortably past the limit,
	// so "it survived" can only mean the limit wasn't enforced.
	allocMB = 512
)

// TestMemoryLimitOOM is the real-boundary test for the resource-limit primitive
// (milestone 2): it proves the cgroup's memory.max actually enforces -- i.e. a
// process that allocates past the limit gets OOM-killed at the CGROUP limit, not
// the host's total memory.
//
// The proof has two halves, and both matter. That the child died by SIGKILL only
// shows something killed it; reading oom_kill out of the group's own
// memory.events is what attributes the kill to THIS cgroup's limit rather than
// to host memory pressure.
//
// This test can only run as root on a cgroup v2 host (i.e. inside
// `make dev-shell` / `make docker-test`, or on a Linux box), so it skips cleanly
// anywhere else instead of failing spuriously.
func TestMemoryLimitOOM(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root to create cgroups; run inside `make dev-shell` or on a Linux host as root")
	}
	if _, err := os.Stat("/sys/fs/cgroup/cgroup.controllers"); err != nil {
		t.Skip("cgroup v2 unified hierarchy not mounted at /sys/fs/cgroup; skipping")
	}

	cgroup, err := New(fmt.Sprintf("minirun-test-%d", os.Getpid()), limitMB<<20, 0)
	if err != nil {
		t.Fatalf("create cgroup: %v", err)
	}
	t.Cleanup(func() {
		if err := cgroup.Cleanup(); err != nil {
			t.Errorf("cleanup cgroup: %v", err)
		}
	})

	// The helper blocks on stdin before allocating a single byte, which closes
	// the same race main.go's parent has to live with: a process can only be put
	// in a cgroup once it has a PID, i.e. once it is already running.
	hog := exec.Command(os.Args[0], "-test.run=^TestHelperMemoryHog$")
	hog.Env = append(os.Environ(), hogEnv+"="+strconv.Itoa(allocMB))
	stdin, err := hog.StdinPipe()
	if err != nil {
		t.Fatalf("stdin pipe: %v", err)
	}
	var output bytes.Buffer
	hog.Stdout = &output
	hog.Stderr = &output

	if err := hog.Start(); err != nil {
		t.Fatalf("start memory hog: %v", err)
	}
	defer func() {
		// If we bail out before Wait, don't leave the helper holding the cgroup
		// open -- Cleanup's rmdir would fail with EBUSY.
		if hog.ProcessState == nil {
			_ = hog.Process.Kill()
			_ = hog.Wait()
		}
	}()

	if err := cgroup.AddProcess(hog.Process.Pid); err != nil {
		t.Fatalf("add hog to cgroup: %v", err)
	}
	if err := assertMember(cgroup, hog.Process.Pid); err != nil {
		t.Fatalf("%v", err)
	}

	// Release the helper: from here it allocates until the kernel stops it.
	if _, err := stdin.Write([]byte("go\n")); err != nil {
		t.Fatalf("release memory hog: %v", err)
	}
	stdin.Close()

	err = hog.Wait()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("hog allocating %d MB inside a %d MB cgroup exited cleanly (%v) -- memory.max did not enforce.\nhelper output:\n%s",
			allocMB, limitMB, err, output.String())
	}
	status, ok := exitErr.Sys().(syscall.WaitStatus)
	if !ok {
		t.Fatalf("unexpected wait status type %T", exitErr.Sys())
	}
	if !status.Signaled() || status.Signal() != syscall.SIGKILL {
		t.Fatalf("expected SIGKILL from the cgroup OOM killer, got %v.\nhelper output:\n%s", exitErr, output.String())
	}

	// The kill was by SIGKILL -- but was it OUR limit that caused it? The kernel
	// counts per-group OOM kills in memory.events, so a non-zero oom_kill here is
	// the cgroup itself reporting that it did this.
	kills, err := eventCount(cgroup, "oom_kill")
	if err != nil {
		t.Fatalf("read memory.events: %v", err)
	}
	if kills == 0 {
		t.Fatalf("hog was SIGKILLed but this cgroup reports oom_kill=0 -- the kill came from outside the group (host pressure?), which is not what this test is proving")
	}
	t.Logf("memory.max=%dMB enforced: helper SIGKILLed after the group recorded %d OOM kill(s)", limitMB, kills)
}

// TestHelperMemoryHog is not an independent test: it is the memory-hog child
// process TestMemoryLimitOOM re-execs. Go's test binary is the most convenient
// "known-good allocator" available to us, so we re-exec ourselves rather than
// depend on a shell one-liner behaving the same on every distro.
func TestHelperMemoryHog(t *testing.T) {
	size := os.Getenv(hogEnv)
	if size == "" {
		t.Skip("helper process; only meaningful when re-execed by TestMemoryLimitOOM")
	}
	megabytes, err := strconv.Atoi(size)
	if err != nil {
		t.Fatalf("bad %s=%q: %v", hogEnv, size, err)
	}

	// Block until the parent says it has put us in the cgroup.
	if _, err := bufio.NewReader(os.Stdin).ReadString('\n'); err != nil {
		t.Fatalf("wait for parent: %v", err)
	}

	held := make([][]byte, 0, megabytes)
	for i := range megabytes {
		chunk := make([]byte, 1<<20)
		// Touch every page: untouched pages are never faulted in, so they cost
		// no physical memory and the cgroup would never notice them.
		for offset := 0; offset < len(chunk); offset += os.Getpagesize() {
			chunk[offset] = byte(i)
		}
		held = append(held, chunk)
	}
	runtime.KeepAlive(held)

	// Reaching here means we allocated the whole thing. The parent treats a
	// clean exit as failure, but say why in the output it captures.
	t.Errorf("allocated %d MB without being OOM-killed", megabytes)
}

// assertMember verifies the kernel really moved pid into the group -- writing to
// cgroup.procs can succeed while the process lands elsewhere if it exits first.
func assertMember(cgroup *Cgroup, pid int) error {
	content, err := os.ReadFile(filepath.Join(cgroup.Path(), "cgroup.procs"))
	if err != nil {
		return fmt.Errorf("read cgroup.procs: %w", err)
	}
	want := strconv.Itoa(pid)
	if !containsField(string(content), want) {
		return fmt.Errorf("pid %d not in %s/cgroup.procs (contains %q)", pid, cgroup.Path(), strings.TrimSpace(string(content)))
	}
	return nil
}

func containsField(content, want string) bool {
	for _, line := range strings.Fields(content) {
		if line == want {
			return true
		}
	}
	return false
}

// eventCount reads one counter out of the group's memory.events, whose format is
// one "key value" pair per line.
func eventCount(cgroup *Cgroup, key string) (int, error) {
	content, err := os.ReadFile(filepath.Join(cgroup.Path(), "memory.events"))
	if err != nil {
		return 0, err
	}
	for _, line := range strings.Split(string(content), "\n") {
		name, value, found := strings.Cut(strings.TrimSpace(line), " ")
		if !found || name != key {
			continue
		}
		return strconv.Atoi(value)
	}
	return 0, fmt.Errorf("%q not present in memory.events", key)
}
