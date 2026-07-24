//go:build linux

// Package cgroups holds the resource-limit primitive: a thin wrapper over a
// cgroup v2 group used to cap the contained process's memory and CPU.
package cgroups

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

const (
	// unifiedRoot is where the cgroup v2 unified hierarchy is mounted. Unlike
	// cgroups v1 -- which mounted one hierarchy per controller under
	// /sys/fs/cgroup/{memory,cpu,pids,...} -- v2 has a single tree and every
	// controller applies to the same set of groups.
	unifiedRoot = "/sys/fs/cgroup"

	// parentName is the intermediate group all minirun containers live under, so
	// the host's tree gets one tidy /sys/fs/cgroup/minirun subtree rather than a
	// scattering of top-level groups. runc does the same thing with its
	// configured cgroup parent path.
	parentName = "minirun"

	// cpuPeriod is the accounting window for cpu.max, in microseconds. The
	// kernel default is 100ms; a quota of 50000 against it means "half a core".
	cpuPeriod = 100_000

	// minCPUQuota is the kernel's smallest accepted cpu.max quota (1ms).
	minCPUQuota = 1_000
)

// ErrNotUnified is returned when the host isn't running cgroups v2, which
// minirun requires -- there is no v1 fallback (see the README's non-goals).
var ErrNotUnified = errors.New("cgroup v2 unified hierarchy not mounted at " + unifiedRoot)

// Cgroup is one created cgroup v2 group, living at a directory under the unified
// hierarchy (mounted at /sys/fs/cgroup on a cgroup v2 host). Everything about
// cgroup v2 is a filesystem: you create a group by mkdir'ing a directory, set
// limits by writing to files in it, add a process by writing its PID to
// cgroup.procs, and delete the group by rmdir'ing it.
type Cgroup struct {
	// path is the absolute path to this group's directory, e.g.
	// /sys/fs/cgroup/minirun/minirun-1234.
	path string
}

// New creates the cgroup /sys/fs/cgroup/minirun/<id> and applies the limits. A
// memLimitBytes or cpuMax of 0 means "no limit for that resource".
func New(id string, memLimitBytes int64, cpuMax float64) (*Cgroup, error) {
	if _, err := os.Stat(filepath.Join(unifiedRoot, "cgroup.controllers")); err != nil {
		return nil, ErrNotUnified
	}

	parent := filepath.Join(unifiedRoot, parentName)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return nil, fmt.Errorf("create parent cgroup %s: %w", parent, err)
	}

	// Delegation: in cgroup v2 a controller's files (memory.max, cpu.max) only
	// appear in a group if that group's PARENT has enabled the controller in its
	// cgroup.subtree_control. So to get memory.max in .../minirun/<id>, both the
	// root and .../minirun must delegate memory downward. Skipping this is the
	// usual cause of "ENOENT writing memory.max" on an otherwise valid path.
	//
	// Only the controllers this container actually needs get delegated: enabling
	// one can fail for reasons particular to the host, and there's no reason a
	// memory-only container should trip over the cpu controller.
	needed := make([]string, 0, 2)
	if memLimitBytes > 0 {
		needed = append(needed, "memory")
	}
	if cpuMax > 0 {
		needed = append(needed, "cpu")
	}
	for _, dir := range []string{unifiedRoot, parent} {
		if err := delegateControllers(dir, needed); err != nil {
			return nil, err
		}
	}

	path := filepath.Join(parent, id)
	if err := os.MkdirAll(path, 0o755); err != nil {
		return nil, fmt.Errorf("create cgroup %s: %w", path, err)
	}
	cgroup := &Cgroup{path: path}

	if err := cgroup.applyLimits(memLimitBytes, cpuMax); err != nil {
		// Don't leave a half-configured group behind on the host.
		_ = cgroup.Cleanup()
		return nil, err
	}
	return cgroup, nil
}

// applyLimits writes the memory and CPU caps into the group's control files.
func (cgroup *Cgroup) applyLimits(memLimitBytes int64, cpuMax float64) error {
	if memLimitBytes > 0 {
		if err := cgroup.writeControl("memory.max", strconv.FormatInt(memLimitBytes, 10)); err != nil {
			return err
		}
		// Without this the kernel can honour memory.max by swapping instead of
		// OOM-killing, which makes the limit a performance cliff rather than a
		// hard cap. Pinning swap to 0 is what `docker run -m X --memory-swap X`
		// does. The file is absent when the kernel has no swap accounting, which
		// is fine -- there's nothing to cap in that case.
		if err := cgroup.writeControl("memory.swap.max", "0"); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if cpuMax > 0 {
		quota := int64(cpuMax * cpuPeriod)
		if quota < minCPUQuota {
			quota = minCPUQuota
		}
		// cpu.max is "<quota> <period>": the group may use <quota> microseconds
		// of CPU time per <period> microseconds of wall clock. The literal
		// string "max" (the default) means unlimited.
		if err := cgroup.writeControl("cpu.max", fmt.Sprintf("%d %d", quota, cpuPeriod)); err != nil {
			return err
		}
	}
	return nil
}

// AddProcess moves the given PID into this cgroup, so the limits start applying
// to it (and, in cgroup v2, to all its descendants -- a process can only ever be
// in one group in the unified hierarchy, and children inherit their parent's).
func (cgroup *Cgroup) AddProcess(pid int) error {
	return cgroup.writeControl("cgroup.procs", strconv.Itoa(pid))
}

// Cleanup removes the cgroup directory once the contained process has exited. A
// cgroup can only be rmdir'd once it has no member processes, which is why the
// parent calls this after child.Wait(). Removing an already-gone group is not an
// error, so this is safe to call twice.
func (cgroup *Cgroup) Cleanup() error {
	if cgroup == nil || cgroup.path == "" {
		return nil
	}
	if err := os.Remove(cgroup.path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove cgroup %s: %w", cgroup.path, err)
	}
	return nil
}

// Path returns the group's directory under the unified hierarchy. Useful for
// reading back kernel-maintained state such as memory.events.
func (cgroup *Cgroup) Path() string { return cgroup.path }

// writeControl writes value to one of this group's control files. Control files
// take a single write of the whole value -- there's no append, and the kernel
// parses each write in full, so this is deliberately not buffered.
func (cgroup *Cgroup) writeControl(name, value string) error {
	file := filepath.Join(cgroup.path, name)
	if err := os.WriteFile(file, []byte(value), 0o644); err != nil {
		return fmt.Errorf("write %q to %s: %w", value, file, err)
	}
	return nil
}

// delegateControllers enables the named controllers in dir's subtree_control, so
// that child groups of dir get those controllers' files. It only ever adds
// controllers that dir itself has available and hasn't already enabled, so it is
// idempotent and never takes a controller away from the host.
func delegateControllers(dir string, wanted []string) error {
	if len(wanted) == 0 {
		return nil
	}
	available, err := readFields(filepath.Join(dir, "cgroup.controllers"))
	if err != nil {
		return fmt.Errorf("read available controllers in %s: %w", dir, err)
	}
	enabled, err := readFields(filepath.Join(dir, "cgroup.subtree_control"))
	if err != nil {
		return fmt.Errorf("read enabled controllers in %s: %w", dir, err)
	}

	for _, controller := range wanted {
		if !slices.Contains(available, controller) || slices.Contains(enabled, controller) {
			continue
		}
		file := filepath.Join(dir, "cgroup.subtree_control")
		if err := os.WriteFile(file, []byte("+"+controller), 0o644); err != nil {
			return fmt.Errorf("enable %q controller in %s: %w", controller, file, err)
		}
	}
	return nil
}

// readFields reads a whitespace-separated cgroup control file into its fields.
func readFields(path string) ([]string, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return strings.Fields(string(content)), nil
}
