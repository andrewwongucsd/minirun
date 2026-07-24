# How minirun maps to a real container runtime

minirun implements three kernel primitives -- namespaces, cgroups v2, and
`pivot_root` -- in about 250 lines. A production runtime (`runc`, or
containerd's `libcontainer`) uses the *same three syscall families* for the same
jobs. What separates them is not the primitives; it's everything layered around
them. This document walks each primitive, points at where minirun does it, says
what `runc` does differently and why, and ends with the list of things minirun
deliberately doesn't do -- which is the honest answer to "is this a sandbox?"
(it isn't).

---

## 1. Namespaces

**What minirun does:** [`internal/namespaces/namespaces.go`](internal/namespaces/namespaces.go)
sets four `clone(2)` flags on the child process:

| Flag | Isolates | Observable effect |
|---|---|---|
| `CLONE_NEWPID` | the PID number space | the contained command is PID 1; host PIDs are invisible |
| `CLONE_NEWUTS` | hostname + domainname | `hostname` says `minirun`, host's is untouched |
| `CLONE_NEWNS` | the mount table | `pivot_root` and the `/proc` mount stay inside |
| `CLONE_NEWIPC` | System V IPC, POSIX message queues | no shared-memory channel to host processes |

**The structural fact: why there are two processes.** Entering a new PID
namespace only ever affects *future children* -- `unshare(CLONE_NEWPID)` does not
move the caller, it changes where the caller's next `fork` lands. So a runtime
cannot namespace itself; it must spawn something. minirun re-execs its own binary
(`/proc/self/exe`) with a hidden `__child` subcommand, and that second process is
PID 1 of the new namespace. Everything about the parent/child split in
[`cmd/minirun/main.go`](cmd/minirun/main.go) follows from this one constraint.

**How `runc` does it:** the same split, with a much sharper tool. `runc create`
re-execs itself as `runc init`, but the namespace setup happens in
`libcontainer/nsenter` -- **C code in a `__attribute__((constructor))`**, which
runs before the Go runtime initializes. That's not stylistic. `setns(2)` and
`unshare(2)` operate on a *single thread*, while the Go runtime is multithreaded
from the moment it starts and freely migrates goroutines between threads. Doing
namespace transitions from Go means you cannot guarantee which thread you
affected. runc sidesteps this by finishing all namespace work while the process
is still single-threaded C, then letting Go start up inside the new namespaces,
receiving its configuration over a pipe.

minirun gets away with pure Go because it only ever *creates* namespaces at
`fork`/`exec` time via `SysProcAttr.Cloneflags`, which the kernel applies in the
child of the clone -- no thread-affinity problem. The moment you want to *join*
an existing container's namespaces (`docker exec`), you need the nsexec approach.

**Being PID 1 is not just a number.** The kernel gives a namespace's init process
special signal handling: a signal sent to it is **discarded unless it installed a
handler for that signal**, with `SIGKILL` and `SIGSTOP` as the exceptions that
are always honoured. This exists so a namespace can't be torn down by accident,
but it has a practical consequence -- a contained `sh -c 'while :; do :; done'`
never handles `SIGTERM`, so asking it to stop does nothing at all.

That's why [`forwardSignals`](cmd/minirun/main.go) relays the first `SIGINT`/
`SIGTERM` to the container and escalates to `SIGKILL` on the second. The parent
can't just die on the signal (that skips the cgroup teardown and orphans the
container), and it can't ignore it either (then `timeout`, systemd or a CI
harness can't stop minirun at all). Real runtimes hit the same wall: this is why
`docker stop` sends `SIGTERM`, waits a grace period, then sends `SIGKILL`, and
why containers whose entrypoint doesn't handle `SIGTERM` always take the full ten
seconds to stop.

**What minirun leaves out:**

- **`CLONE_NEWNET`** -- no network namespace, so the container shares the host's
  interfaces, routes, and localhost. Real runtimes create a netns and hand it to
  CNI plugins to wire up a veth pair into a bridge. Practical consequence: a
  minirun container can reach anything the host can, including services bound to
  `127.0.0.1` and cloud instance-metadata endpoints.
- **`CLONE_NEWUSER`** -- see §4. This is the significant one.
- `CLONE_NEWCGROUP` (hides the host's cgroup paths from `/proc/self/cgroup`) and
  `CLONE_NEWTIME`, both of which runc supports.

---

## 2. cgroups v2

**What minirun does:** [`internal/cgroups/cgroups.go`](internal/cgroups/cgroups.go)
treats the cgroup as what it literally is -- a directory tree. There is no
syscall here at all:

| Operation | Implementation |
|---|---|
| create a group | `mkdir /sys/fs/cgroup/minirun/<id>` |
| cap memory | write `104857600` to `memory.max` |
| cap CPU | write `50000 100000` to `cpu.max` |
| add a process | write the PID to `cgroup.procs` |
| destroy the group | `rmdir` (only succeeds when empty of processes) |

**v2 vs v1.** cgroups v1 mounted one independent hierarchy per controller --
`/sys/fs/cgroup/memory`, `/sys/fs/cgroup/cpu`, and so on -- and a process could
sit in unrelated groups in each. That made coherent resource accounting hard
(the memory controller couldn't cheaply ask about the same group's I/O) and made
runtimes track N paths per container. v2 has a **single unified tree**: one
group per process, every controller applying to the same node. minirun requires
v2 and has no v1 fallback.

**Delegation is the part that surprises everyone.** A controller's files do not
exist in a group unless the group's *parent* enabled that controller in its
`cgroup.subtree_control`. Create `/sys/fs/cgroup/minirun/abc` on a fresh tree and
write `memory.max`, and you get `ENOENT` -- not because the path is wrong, but
because nobody delegated `memory` down to it. `delegateControllers` walks
root → `minirun/` writing `+memory` / `+cpu` at each level. Related: the **"no
internal processes" rule** -- a non-root group may hold processes *or* have
controller-enabled children, never both. That's why containers always live at
leaves of the tree, and why minirun's per-container group is one level below the
`minirun/` parent rather than being it.

**Two details worth knowing.** `memory.max` is a *hard* cap: on breach the kernel
reclaims, and if it can't reclaim enough, it OOM-kills inside the group. (There's
also `memory.high`, a soft cap that throttles rather than kills.) And a memory
cap alone isn't a memory cap if the box has swap -- the kernel will happily swap
to stay under it -- so minirun also pins `memory.swap.max` to `0`, which is
exactly what `docker run -m X --memory-swap X` does. `cpu.max` is CFS bandwidth
control: `"<quota> <period>"` in microseconds, so half a core is `50000 100000`.

**How `runc` does it:** the same writes, behind a `cgroups.Manager` interface
with two implementations. The `fs2` manager writes the files directly, as minirun
does. The **systemd** manager -- what Docker and Kubernetes use by default on
systemd hosts -- instead asks systemd over D-Bus to create a *transient scope
unit*, and lets systemd own the directory. The reason is arbitration: on a
systemd host, systemd considers itself the single owner of the cgroup tree, and
two writers racing to manage the same subtree is how you get limits silently
reverted. minirun writing directly under `/sys/fs/cgroup/minirun/` is the `fs2`
path, and is fine precisely because nothing else claims that subtree.

runc also drives controllers minirun ignores: `pids.max` (fork-bomb defence),
`io.max`, `cpuset.cpus`, the freezer, and device access -- which in v2 is no
longer a cgroup file at all but an **eBPF program** attached to the cgroup.

**The race, and how it's actually solved.** minirun starts the child, *then*
writes its PID to `cgroup.procs` ([main.go](cmd/minirun/main.go)) -- so there's a
window where the child is running unlimited. The kernel's answer is
`clone3(CLONE_INTO_CGROUP)`, which creates the process *directly into* a target
cgroup, closing the window entirely; runc uses it where available and otherwise
gates the child on a synchronisation pipe until the parent has added it. The test
in [`cgroups_test.go`](internal/cgroups/cgroups_test.go) uses the pipe trick to
make the OOM assertion deterministic.

---

## 3. `pivot_root`

**What minirun does:** [`internal/rootfs/rootfs.go`](internal/rootfs/rootfs.go),
inside the new mount namespace, in this order:

1. `mount(MS_REC|MS_PRIVATE)` on `/`
2. bind-mount the rootfs directory onto itself
3. `mkdir` a parking spot for the old root
4. `pivot_root(newRoot, oldRoot)`
5. `chdir("/")`
6. `umount2(MNT_DETACH)` the old root and remove its directory
7. mount a fresh `procfs` at `/proc`

**Why each step exists** -- this is the part that returns `EINVAL` if you improvise:

- **Step 1** because `pivot_root` refuses when the current root is a *shared*
  mount. On a systemd host `/` is `shared` by default, so propagation would also
  push every mount we make back out into the host's mount table.
- **Step 2** because `pivot_root` requires the new root to be a **mount point**.
  A plain directory isn't one; bind-mounting it onto itself makes it one without
  changing what's in it.
- **Step 5** because the process's cwd is still an inode in the *old* root.
  Leaving it there is not cosmetic: a directory handle outside your root is the
  classic chroot-escape foothold (`fchdir` to it, then `cd ..` until you hit the
  real `/`).
- **Step 6** because up to here the host filesystem is merely *moved*, not gone.
  Detaching it is what makes the isolation real. `MNT_DETACH` (lazy) rather than
  a plain unmount because the old root's submounts are still referenced; the tree
  disappears when the last reference does.
- **Step 7** after the pivot and after the PID namespace exists, because procfs
  snapshots the PID namespace of whoever mounts it. Mount it too early and `ps`
  shows the host's process list from inside the container.

**`chroot` vs `pivot_root`.** `chroot` only moves a pointer -- the old root stays
mounted and reachable, which is why "escaping chroot" is a recipe rather than a
CVE. `pivot_root` lets you *unmount* what you came from. Real runtimes use it for
that reason, and so does minirun.

**How `runc` does it:** the same dance with more care.
`libcontainer/rootfs_linux.go` sets propagation (`MS_SLAVE|MS_REC` by default,
so host mount events still propagate *in* but container ones don't leak *out*),
mounts everything `config.json` asks for, then pivots using a neat variant --
`pivot_root(".", ".")` with an fd held on the old root -- which avoids needing a
writable directory inside the container image for the parking spot. It then
masks dangerous paths (`/proc/kcore`, `/proc/timer_list`, `/sys/firmware`) with
bind-mounted `/dev/null`, remounts a set of paths read-only, and finally
`MS_RDONLY`s the whole rootfs if the spec says so. None of that masking exists in
minirun.

**Where the rootfs comes from** is the bigger difference. `runc` doesn't build
one -- it receives an OCI bundle: a directory plus a `config.json`, assembled by
containerd's snapshotter, which unpacks image layers and stacks them into an
**overlayfs** mount so layers are shared copy-on-write between containers.
minirun's `setup-rootfs` ([busybox.go](internal/rootfs/busybox.go)) -- copy one
static busybox, symlink every applet -- stands in for that entire subsystem.
Image formats are an explicit non-goal.

---

## 4. The gaps that make this a learning tool, not a sandbox

Everything above is real isolation against *accidents*: a process that misbehaves,
overallocates, or writes where it shouldn't. None of it is isolation against an
*adversary*. A container built from only these primitives is escapable, and
here's specifically what's missing.

**No user namespace / no rootless.** The contained process runs as real UID 0 --
root on the container is root on the host, with the host's full capability set.
`CLONE_NEWUSER` is what breaks that equivalence: it maps container UID 0 to an
unprivileged host UID, so "root" inside holds capabilities only over resources
the namespace owns. Without it, any path that gets you a host file descriptor or
a mount gets you the machine. This is the single largest gap in minirun.

**No capability drop.** runc starts from the assumption that root inside a
container should not have all of root's powers, and drops everything outside a
default set -- notably `CAP_SYS_ADMIN`, which alone grants `mount`, and therefore
grants "mount the host disk". minirun drops nothing and runs under `--privileged`
for its own dev loop, which means the container holds `CAP_SYS_ADMIN` by
construction. The concrete shape of this: CVE-2022-0492 was an escape that needed
nothing more than the ability to write a cgroup `release_agent` file.

**No seccomp-bpf.** The Linux kernel exposes 300+ syscalls, and the container
reaches all of them. runc installs a seccomp-BPF filter from the OCI spec before
`exec`; Docker's default profile blocks ~44 of them. Syscall filtering matters
because the escape path for untrusted code is rarely the container abstraction --
it's a memory-safety bug in some rarely-exercised kernel syscall, and a filter
that never lets you call it is a filter that makes that bug unreachable.

**No network namespace.** Covered in §1: the container is on the host's network
stack, including loopback.

**No masked/read-only paths.** `/proc/kcore` (a readable image of kernel memory)
and `/sys` are fully exposed inside minirun's `/proc` mount.

**Why this matters for the build-vs-buy question.** These primitives are the
floor, not the ceiling, and that's the whole reason the hardening layers exist:
**gVisor** interposes a user-space kernel so the container's syscalls never reach
the host kernel directly, and **Firecracker / Kata** put a real VM boundary
underneath so an escape has to get through hardware virtualization rather than
through a shared kernel. Both exist because "namespaces + cgroups + pivot_root",
implemented perfectly, is still one kernel bug away from a host compromise.
Building minirun is how you get to make that call from understanding rather than
from a vendor page -- and the conclusion it points at is that if you ever *do*
build in-house, the primitives are the easy part and the hardening layer is the
product.

---

## Try it

```
make dev-shell                                       # privileged Linux container (macOS host)
go run ./cmd/minirun setup-rootfs ./rootfs
go run ./cmd/minirun run --mem=100m --cpu=0.5 ./rootfs -- /bin/sh

/ # echo $$        # 1        -- CLONE_NEWPID
/ # hostname       # minirun  -- CLONE_NEWUTS
/ # ls /           # only the rootfs -- pivot_root
/ # ps             # only container processes -- fresh procfs
```

`go test ./...` runs the OOM-enforcement test, which skips unless it finds root
and a cgroup v2 hierarchy.
