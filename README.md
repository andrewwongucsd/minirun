# minirun

A minimal container runtime built from scratch to learn the Linux kernel
primitives that Docker / containerd / `runc` sit on top of: namespaces
(via `clone(2)` flags), cgroups v2 for resource limits, and `chroot`/`pivot_root`
for filesystem isolation.

```
minirun run --mem=100m --cpu=0.5 ./rootfs -- /bin/sh
```

...gives you a shell that thinks it's PID 1, has its own hostname, sees only the
files under `./rootfs`, and is capped at 100 MB of memory and half a CPU.

This is a **learning prototype**, not production isolation, and explicitly **not
a resolution** of the "sandboxed code execution: build vs. buy" question it feeds
(see the repo's `ROADMAP.md` Product 2 section). It deliberately omits the pieces
that separate "understands the primitives" from "safe to run a stranger's
adversarial code": **no seccomp-bpf, no rootless/user namespaces**, no network
isolation. Raw namespaces + chroot have a real history of container escapes,
which is exactly why gVisor and Firecracker exist as hardening layers on top of
these same primitives. The point of building this is to make the build-vs-buy
call from hands-on understanding -- if "build in-house" ever wins, real
sandboxing still needs a hardening layer beyond what's here.

> **Status:** scaffolded. The CLI wiring, the parent/child re-exec dispatch, and
> the busybox rootfs packaging are complete; every actual kernel-primitive call
> (namespace clone flags, hostname, pivot_root, cgroup writes) is a stub. Work
> through the milestones below -- see the repo-root `TODO.md` for the live
> checklist. Update this README's diagrams as the real code diverges (per the
> repo's `AGENTS.md` rule).

## This only runs on Linux

cgroups v2 and the namespace `clone` flags are Linux kernel features -- the code
uses `syscall`/`golang.org/x/sys`-style Linux constructs that don't exist on
macOS. **Every `.go` file here carries a `//go:build linux` tag**, so on a Mac,
`go build ./...` inside this module is a deliberate no-op (it builds nothing
rather than erroring). See "Local dev loop" for how to actually run it.

## Local dev loop (developing on macOS)

You do **not** need a cloud VM. Docker Desktop's own Linux VM has a real kernel
with cgroups v2 -- verified: `docker info` reports `Cgroup Version: 2`,
`Kernel Version: 6.10.14-linuxkit`. It's the same mechanism `kind` uses to run
Kubernetes-in-Docker on a Mac. The `Makefile` drives a privileged dev container
with the repo bind-mounted (so your host edits are live, no image rebuild):

```
make dev-image      # build the golang:1.26-bookworm dev image once
make dev-shell      # drop into a --privileged root shell in the container,
                    # cwd = this module -- run `go run`, `go test`, etc. here
make docker-test    # one-shot: run `go test ./...` inside the container
make vet-typecheck  # NO container: cross-vet from the Mac host to type-check
                    #   the Linux code (GOOS=linux go vet ./...)
```

Type-check-while-you-edit on the Mac host with `make vet-typecheck`; actually
*run* namespace/cgroup code inside `make dev-shell`. `gofmt` works either place.

Why `--privileged --cgroupns=host` (see the Makefile): `--privileged` grants
`CAP_SYS_ADMIN` (needed for `unshare`/`clone` with namespace flags and for
`pivot_root`) and drops the default seccomp filter that would otherwise block
those syscalls; `--cgroupns=host` gives the container the real top-level
`/sys/fs/cgroup` tree so `mkdir /sys/fs/cgroup/minirun/<id>` behaves the way it
would on bare metal (like `runc`).

### Known first blocker: `pivot_root` returns EINVAL

`pivot_root` refuses to work unless the new root is a mount point and the current
root's mount is not shared. The standard dance (do it in this order, inside the
new mount namespace) is: make the whole mount tree private
(`mount --make-rprivate /`), bind-mount the rootfs directory onto itself so it
becomes a mount point, `pivot_root` into it, `chdir("/")`, then unmount the old
root and remove its temp dir. This isn't specific to minirun -- it's how every
runtime uses `pivot_root` -- but it's the thing that trips everyone up first.

## Architecture

Packages, from the entrypoint down to the primitives. Every file is
`//go:build linux`.

```
======================================================================
 cmd/minirun/main.go     the CLI + the parent/child re-exec dispatch
   parent ("run"):   parse flags, make cgroup, re-exec self in new
                     namespaces, put child in cgroup, wait, clean up
   child ("__child"): set hostname, pivot_root, exec the user command
 depends on: internal/namespaces, internal/rootfs, internal/cgroups
======================================================================
 internal/namespaces     the isolation primitive
   namespaces.go   Command() builds the re-exec with Cloneflags;
                   SetHostname()                                   (M1)
----------------------------------------------------------------------
 internal/rootfs         the filesystem-illusion primitive
   rootfs.go       PivotInto() -- chroot/pivot_root dance          (M1)
   busybox.go      fetch + lay out a static-busybox rootfs (COMPLETE)
----------------------------------------------------------------------
 internal/cgroups        the resource-limit primitive
   cgroups.go      New()/AddProcess()/Cleanup() over cgroup v2     (M2)
======================================================================
```

## Data flow

One `minirun run` invocation, parent process then child process. The two-stage
re-exec exists because a process **cannot** move itself into a new PID namespace
-- only its *children* get placed there -- so `minirun` has to launch a second
copy of itself with the clone flags set.

```
  $ minirun run --mem=100m --cpu=0.5 ./rootfs -- /bin/sh
        |
   [ parent: cmd/minirun, subcommand "run" ]
        | parse flags -> memBytes, cpuMax, rootfsPath, command
        v
   cgroups.New(id, memBytes, cpuMax)          -- mkdir /sys/fs/cgroup/minirun/<id>,
        |                                        write memory.max, cpu.max
        v
   namespaces.Command("__child", rootfs, cmd) -- exec.Cmd re-execing /proc/self/exe
        |                                        with SysProcAttr.Cloneflags:
        |                                        NEWUTS|NEWPID|NEWNS|NEWIPC
        v
   child.Start()  ---------------------------------------------.
        |                                                       |
   cgroups.AddProcess(child.Pid)  -- echo pid > cgroup.procs    |
        |                                                       v
        |                                  [ child: subcommand "__child", PID 1 ]
        |                                        | namespaces.SetHostname("minirun")
        |                                        v
        |                                  rootfs.PivotInto(rootfsPath)
        |                                        | (the pivot_root dance)
        |                                        v
        |                                  syscall.Exec(command)  -- become /bin/sh
        v                                        |
   child.Wait()  <--------- process exits -------'
        |
   cgroups.Cleanup()  -- rmdir the cgroup
```

## Non-goals (deliberate)

- **No OCI image format, no image pulling** -- the "image" is a plain directory
  you point at (build one with `make rootfs`, which lays out static busybox).
- **No networking** -- no veth, no bridge, no network namespace. The container
  shares the host network.
- **No rootless / user namespaces** -- runs as root (that's why the dev container
  is `--privileged`). Rootless is a whole additional layer, deliberately skipped.
- **No seccomp** -- no syscall filtering. This is one of the reasons this isn't
  safe for genuinely untrusted code (see the top of this README).
- **No daemon, no API** -- one CLI invocation runs one command.

## Milestones

1. **Namespaces + chroot** -- `internal/namespaces` (clone flags + hostname) and
   `internal/rootfs/rootfs.go` (pivot_root). Success: a shell that reports
   `echo $$` -> 1 and its own `hostname`, seeing only the rootfs.
2. **cgroups v2 limits** -- `internal/cgroups`. Success: run a memory hog inside
   `--mem=50m` and watch the OOM killer fire at the cgroup limit, not the host's
   (that's `cgroups_test.go`).
3. **CLI polish + rootfs packaging + cleanup** -- finalize flags, wire
   `busybox.go` into a `make rootfs` target, ensure the cgroup and mounts are
   cleaned up on exit.
4. **The explainer** -- write `EXPLAINER.md` mapping each primitive to what
   `runc`/containerd's `libcontainer` actually does. Writing it is the deliverable.
