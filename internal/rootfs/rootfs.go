//go:build linux

// Package rootfs holds the filesystem-isolation primitive (swapping the root
// filesystem so the contained process only sees the image) and the busybox
// rootfs packaging helper (see busybox.go, which is already complete).
package rootfs

// PivotInto makes newRoot the process's root filesystem, so afterwards "/" is
// newRoot and the host's filesystem is no longer reachable. It must run in the
// child, inside the new mount namespace (CLONE_NEWNS), before the final exec.
//
// Milestone 1 -- implement the pivot_root dance (order matters; this is the part
// that returns EINVAL if you skip a step):
//
//  1. Make mount propagation private for the whole tree:
//     syscall.Mount("", "/", "", syscall.MS_REC|syscall.MS_PRIVATE, "").
//     Without this, pivot_root refuses because "/" is a shared mount.
//  2. Bind-mount newRoot onto itself:
//     syscall.Mount(newRoot, newRoot, "", syscall.MS_BIND|syscall.MS_REC, "").
//     pivot_root requires the new root to be a mount point, not just a directory.
//  3. Make a place for the old root, e.g. newRoot+"/.oldroot" (os.MkdirAll).
//  4. syscall.PivotRoot(newRoot, oldrootPath).
//  5. os.Chdir("/") so the cwd isn't dangling in the old root.
//  6. Unmount the old root (syscall.Unmount("/.oldroot", syscall.MNT_DETACH))
//     and remove the "/.oldroot" dir, so the host filesystem is fully detached.
//
// Optional but useful once this works: mount a fresh /proc inside
// (syscall.Mount("proc", "/proc", "proc", 0, "")) so `ps`/`echo $$` see the new
// PID namespace. That needs CLONE_NEWPID (which namespaces.Command sets) to be
// meaningful.
//
// A note on chroot vs pivot_root: chroot alone is weaker (the old root can be
// escaped in various well-known ways), which is exactly why real runtimes use
// pivot_root and detach the old root. Implementing pivot_root is the point;
// mentioning why in EXPLAINER.md is milestone 4.
func PivotInto(newRoot string) error {
	panic("not implemented -- milestone 1: pivot_root into newRoot")
}
