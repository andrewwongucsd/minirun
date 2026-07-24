//go:build linux

// Command minirun is a minimal container runtime. See the module README for the
// full picture; the short version is:
//
//	minirun run --mem=100m --cpu=0.5 ./rootfs -- /bin/sh
//
// runs /bin/sh isolated in new namespaces, rooted at ./rootfs, capped by a
// cgroup. There's also `minirun setup-rootfs ./rootfs` to build a busybox rootfs.
//
// This file is complete scaffolding: it owns the control flow -- argument
// parsing and the parent/child re-exec dance -- and delegates every actual
// kernel operation to the internal packages (which are the stubs you implement).
// The one structural thing to understand here is the re-exec: a process can't
// put ITSELF into a new PID namespace, only its children, so `minirun run`
// (the parent) launches a second copy of this same binary via the hidden
// `__child` subcommand, and it's that child that comes up as PID 1 inside the
// new namespaces.
package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/andrewwongucsd/minirun/internal/cgroups"
	"github.com/andrewwongucsd/minirun/internal/namespaces"
	"github.com/andrewwongucsd/minirun/internal/rootfs"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	switch os.Args[1] {
	case "run":
		// runParent returns rather than exiting so its deferred cgroup cleanup
		// actually runs; os.Exit belongs out here, past the last defer.
		os.Exit(runParent(os.Args[2:]))
	case "__child":
		// Internal re-exec target -- not meant to be run by hand. This is the
		// process that actually lives inside the new namespaces.
		runChild(os.Args[2:])
	case "setup-rootfs":
		setupRootfs(os.Args[2:])
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, `minirun -- a minimal container runtime (learning prototype)

usage:
  minirun run [--mem=SIZE] [--cpu=CORES] <rootfs-path> -- <command> [args...]
  minirun setup-rootfs [dir]     build a busybox rootfs (default ./rootfs)

examples:
  minirun setup-rootfs ./rootfs
  minirun run --mem=100m --cpu=0.5 ./rootfs -- /bin/sh
`)
	os.Exit(2)
}

// runParent is the `minirun run ...` entry point: set up the cgroup, re-exec
// this binary into new namespaces, put the child in the cgroup, and wait. It
// returns the exit code for the whole invocation instead of calling os.Exit,
// because os.Exit skips deferred functions -- and the cgroup teardown below is a
// deferred function that has to run on every path, including a failed container.
func runParent(args []string) int {
	memStr, cpu, rootfsPath, command := parseRunArgs(args)

	memBytes, err := parseMem(memStr)
	if err != nil {
		fatal("parse --mem: %v", err)
	}

	id := fmt.Sprintf("minirun-%d", os.Getpid())
	cgroup, err := cgroups.New(id, memBytes, cpu)
	if err != nil {
		fatal("create cgroup: %v", err)
	}
	defer func() {
		if err := cgroup.Cleanup(); err != nil {
			warn("cgroup cleanup: %v", err)
		}
	}()

	// Ctrl-C already reaches the container directly -- the child shares our
	// process group and terminal -- so the parent's only job here is to outlive
	// it. Left to the default handler, SIGINT would os.Exit the parent and skip
	// the cleanup deferred above; catching it lets child.Wait() return normally.
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(signals)

	// Re-exec ourselves as the __child subcommand, in new namespaces. The child
	// inherits our stdio so the user sees/drives the contained process directly.
	childArgs := append([]string{"__child", rootfsPath, "--"}, command...)
	child := namespaces.Command(childArgs)
	child.Stdin = os.Stdin
	child.Stdout = os.Stdout
	child.Stderr = os.Stderr

	if err := child.Start(); err != nil {
		warn("start child: %v", err)
		return 1
	}

	// Put the child in the cgroup now that it has a PID. (Note the small race:
	// the child could allocate a little before this lands. Real runtimes close
	// it by creating the process already-in-cgroup via clone3's CLONE_INTO_CGROUP
	// or by gating the child on a pipe until it's been added -- a good later
	// refinement, out of scope for the first pass.)
	if err := cgroup.AddProcess(child.Process.Pid); err != nil {
		warn("add child to cgroup: %v", err)
	}

	if err := child.Wait(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return exitCode(exitErr)
		}
		warn("wait for child: %v", err)
		return 1
	}
	return 0
}

// exitCode turns a finished child's state into a shell exit status. A process
// killed by signal N reports 128+N, the same convention `docker run` follows --
// which is why a container that blows its memory.max surfaces as the familiar
// 137 (128 + SIGKILL) rather than a bare -1.
func exitCode(exitErr *exec.ExitError) int {
	if status, ok := exitErr.Sys().(syscall.WaitStatus); ok && status.Signaled() {
		return 128 + int(status.Signal())
	}
	return exitErr.ExitCode()
}

// runChild runs inside the new namespaces (as PID 1). It sets the hostname,
// swaps the root filesystem, and finally replaces itself with the user's
// command. args is [rootfsPath, "--", command...].
func runChild(args []string) {
	rootfsPath, command := splitAtDashDash(args)
	if rootfsPath == "" || len(command) == 0 {
		fatal("internal __child: bad args %q", args)
	}

	if err := namespaces.SetHostname("minirun"); err != nil {
		fatal("set hostname: %v", err)
	}
	if err := rootfs.PivotInto(rootfsPath); err != nil {
		fatal("pivot into rootfs: %v", err)
	}

	// Replace this process image with the user's command so it truly becomes
	// PID 1's program (rather than a child of it). After PivotInto, paths are
	// relative to the new root.
	path, err := resolveCommand(command[0])
	if err != nil {
		fatal("resolve command %q: %v", command[0], err)
	}
	if err := syscall.Exec(path, command, os.Environ()); err != nil {
		fatal("exec %q: %v", path, err)
	}
}

// setupRootfs builds a busybox rootfs at the given dir (default ./rootfs).
func setupRootfs(args []string) {
	dir := "./rootfs"
	if len(args) > 0 {
		dir = args[0]
	}
	if err := rootfs.SetupBusybox(dir); err != nil {
		fatal("setup rootfs: %v", err)
	}
	fmt.Printf("rootfs ready at %s -- try: minirun run %s -- /bin/sh\n", dir, dir)
}

// parseRunArgs splits `[--mem=..] [--cpu=..] <rootfs> -- <command...>`. Kept as
// hand-rolled parsing (rather than flag.FlagSet) so the "--" separator between
// our flags and the contained command is unambiguous.
func parseRunArgs(args []string) (memStr string, cpu float64, rootfsPath string, command []string) {
	memStr = "0" // 0 => no memory limit
	cpu = 0      // 0 => no cpu limit
	i := 0
	for ; i < len(args); i++ {
		arg := args[i]
		switch {
		case strings.HasPrefix(arg, "--mem="):
			memStr = strings.TrimPrefix(arg, "--mem=")
		case strings.HasPrefix(arg, "--cpu="):
			value, err := strconv.ParseFloat(strings.TrimPrefix(arg, "--cpu="), 64)
			if err != nil {
				fatal("parse --cpu: %v", err)
			}
			cpu = value
		case !strings.HasPrefix(arg, "--"):
			// first positional: the rootfs path
			rootfsPath = arg
			i++
			goto rest
		default:
			fatal("unknown flag %q", arg)
		}
	}
rest:
	// remaining args should be: -- <command...>
	remaining := args[i:]
	command = afterDashDash(remaining)
	if rootfsPath == "" {
		fatal("missing <rootfs-path>")
	}
	if len(command) == 0 {
		fatal("missing command (did you forget `-- <command>`?)")
	}
	return memStr, cpu, rootfsPath, command
}

// afterDashDash returns everything after the first "--" element.
func afterDashDash(args []string) []string {
	for index, arg := range args {
		if arg == "--" {
			return args[index+1:]
		}
	}
	return nil
}

// splitAtDashDash splits [head, "--", tail...] into (head, tail).
func splitAtDashDash(args []string) (head string, tail []string) {
	if len(args) == 0 {
		return "", nil
	}
	return args[0], afterDashDash(args)
}

// resolveCommand finds an executable path for name within the (already pivoted)
// root: absolute paths are used as-is, bare names are looked up in the usual
// busybox locations.
func resolveCommand(name string) (string, error) {
	if filepath.IsAbs(name) {
		return name, nil
	}
	for _, dir := range []string{"/bin", "/usr/bin", "/sbin", "/usr/sbin"} {
		candidate := filepath.Join(dir, name)
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("not found in PATH-like dirs")
}

// parseMem parses a size like "100m", "1g", "512k", or a plain byte count into
// bytes. "0" or "" means no limit.
func parseMem(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" || s == "0" {
		return 0, nil
	}
	multiplier := int64(1)
	switch last := s[len(s)-1]; last {
	case 'k', 'K':
		multiplier = 1 << 10
		s = s[:len(s)-1]
	case 'm', 'M':
		multiplier = 1 << 20
		s = s[:len(s)-1]
	case 'g', 'G':
		multiplier = 1 << 30
		s = s[:len(s)-1]
	}
	value, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, err
	}
	return value * multiplier, nil
}

// warn reports a non-fatal problem and lets the caller decide what to do -- used
// for anything that happens after the cgroup exists, where exiting outright
// would skip its teardown.
func warn(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "minirun: warning: "+format+"\n", args...)
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "minirun: "+format+"\n", args...)
	os.Exit(1)
}
