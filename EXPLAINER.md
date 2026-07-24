# How minirun maps to a real container runtime

> **Milestone 4 deliverable -- not yet written.** This file is the interview
> talking-point script: once the runtime works, write up each primitive minirun
> implements and connect it to what a production runtime (`runc` /
> containerd's `libcontainer`) does with the same kernel feature. Writing this
> *is* the milestone -- it's how you turn "I made the syscalls" into "I can
> explain what Docker abstracts away."

## To cover (fill in as you implement each piece)

- **Namespaces (`CLONE_NEWUTS|NEWPID|NEWNS|NEWIPC`)** -- what each one isolates,
  why minirun re-execs itself to enter a new PID namespace, and how this maps to
  `runc`'s use of `libcontainer`'s nsexec/clone. What minirun leaves out:
  network and user namespaces.
- **cgroups v2 (`memory.max`, `cpu.max`, `cgroup.procs`)** -- the unified
  hierarchy, how the limit is enforced by the kernel, and how this maps to what
  the runtime's cgroup manager writes. Contrast with cgroups v1.
- **`pivot_root` / `chroot`** -- how swapping the root mount creates the "you
  only see the image" illusion, the mount-propagation dance required, and how
  this maps to how a real runtime sets up the container rootfs from an unpacked
  OCI image.
- **The gaps that make this a learning tool, not a sandbox** -- no seccomp, no
  rootless/user-namespace mapping, no capabilities drop, no network isolation --
  and what each of those would add. This section is what keeps the resume/
  interview framing honest.
