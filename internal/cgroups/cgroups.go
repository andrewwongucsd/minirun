//go:build linux

// Package cgroups holds the resource-limit primitive: a thin wrapper over a
// cgroup v2 group used to cap the contained process's memory and CPU.
package cgroups

// Cgroup is one created cgroup v2 group, living at a directory under the unified
// hierarchy (mounted at /sys/fs/cgroup on a cgroup v2 host). Everything about
// cgroup v2 is a filesystem: you create a group by mkdir'ing a directory, set
// limits by writing to files in it, add a process by writing its PID to
// cgroup.procs, and delete the group by rmdir'ing it.
type Cgroup struct {
	// path is the absolute path to this group's directory, e.g.
	// /sys/fs/cgroup/minirun/minirun-1234. Set it in New.
	path string
}

// New creates the cgroup /sys/fs/cgroup/minirun/<id> and applies the limits.
//
// Milestone 2 -- implement:
//   - path := "/sys/fs/cgroup/minirun/" + id ; os.MkdirAll(path, 0755).
//     (Creating the intermediate "minirun" group is fine; the kernel makes it a
//     real cgroup too.)
//   - If memLimitBytes > 0, write it as a decimal string to path+"/memory.max".
//   - If cpuMax > 0, write CPU as "<quota> <period>" to path+"/cpu.max", e.g.
//     0.5 cores with the default 100000us period is "50000 100000". "max"
//     (the default) means unlimited.
//   - Return &Cgroup{path: path}.
//
// You may hit the cgroup v2 "no internal processes" / controller-delegation
// rules depending on where you create the group; if writing memory.max fails
// with ENOENT for the controller, you likely need the parent's
// cgroup.subtree_control to enable "+memory +cpu". Note what you find in
// EXPLAINER.md -- delegation is a real part of how this works in production.
func New(id string, memLimitBytes int64, cpuMax float64) (*Cgroup, error) {
	panic("not implemented -- milestone 2: create cgroup + write memory.max/cpu.max")
}

// AddProcess moves the given PID into this cgroup, so the limits start applying
// to it (and, in cgroup v2, to all its descendants). Milestone 2: write the pid
// as a decimal string to <path>/cgroup.procs.
func (cgroup *Cgroup) AddProcess(pid int) error {
	panic("not implemented -- milestone 2: write pid to cgroup.procs")
}

// Cleanup removes the cgroup directory once the contained process has exited.
// Milestone 2: os.Remove(cgroup.path). A cgroup can only be removed once it has
// no member processes, which is why the parent calls this after child.Wait().
func (cgroup *Cgroup) Cleanup() error {
	panic("not implemented -- milestone 2: rmdir the cgroup")
}
