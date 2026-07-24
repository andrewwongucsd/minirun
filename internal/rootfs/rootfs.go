//go:build linux

// Package rootfs holds the filesystem-isolation primitive (swapping the root
// filesystem so the contained process only sees the image) and the busybox
// rootfs packaging helper (see busybox.go, which is already complete).
package rootfs

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// oldRootName is the directory inside the new root that pivot_root parks the old
// root on. It only exists for the few microseconds between pivot_root and the
// unmount below -- by the time the user's command runs it is gone, so the host
// filesystem is unreachable rather than merely out of the way.
const oldRootName = ".oldroot"

// PivotInto makes newRoot the process's root filesystem, so afterwards "/" is
// newRoot and the host's filesystem is no longer reachable. It must run in the
// child, inside the new mount namespace (CLONE_NEWNS), before the final exec.
//
// The order below is the standard pivot_root dance, and every step is load
// bearing -- skip one and the syscall returns EINVAL:
//
//  1. Make mount propagation private for the whole tree. pivot_root refuses if
//     the current root's mount is shared, and without this our later mounts
//     would propagate back out to the host's mount table.
//  2. Bind-mount newRoot onto itself. pivot_root requires the new root to be a
//     mount point; a plain directory is not one.
//  3. Make a place inside the new root for the old root to be parked.
//  4. pivot_root(2) itself.
//  5. chdir("/") so the cwd isn't left dangling in the old root -- a cwd outside
//     the new root is one of the classic chroot-escape footholds.
//  6. Lazily unmount the old root and remove its directory, fully detaching the
//     host filesystem.
//
// Finally it mounts a fresh procfs, which is what makes `ps` and `echo $$`
// reflect the new PID namespace instead of the host's.
//
// A note on chroot vs pivot_root: chroot alone only moves the root-directory
// pointer -- the old root stays mounted and reachable, which is why "escape from
// chroot" is a well-known recipe. pivot_root lets us detach the old root
// entirely, which is why real runtimes use it. See EXPLAINER.md.
func PivotInto(newRoot string) error {
	newRoot, err := filepath.Abs(newRoot)
	if err != nil {
		return fmt.Errorf("resolve rootfs path: %w", err)
	}
	if info, err := os.Stat(newRoot); err != nil {
		return fmt.Errorf("stat rootfs: %w", err)
	} else if !info.IsDir() {
		return fmt.Errorf("rootfs %s is not a directory", newRoot)
	}

	// 1. Nothing we do to the mount table from here on escapes to the host.
	if err := syscall.Mount("", "/", "", syscall.MS_REC|syscall.MS_PRIVATE, ""); err != nil {
		return fmt.Errorf("make / rprivate: %w", err)
	}

	// 2. Turn the rootfs directory into a mount point by bind-mounting it onto
	//    itself -- pivot_root will not accept anything less.
	if err := syscall.Mount(newRoot, newRoot, "", syscall.MS_BIND|syscall.MS_REC, ""); err != nil {
		return fmt.Errorf("bind-mount %s onto itself: %w", newRoot, err)
	}

	// 3. pivot_root needs somewhere under the new root to hang the old one.
	oldRoot := filepath.Join(newRoot, oldRootName)
	if err := os.MkdirAll(oldRoot, 0o700); err != nil {
		return fmt.Errorf("create old-root mount point: %w", err)
	}

	// 4. Swap the roots. After this "/" is newRoot for this mount namespace.
	if err := syscall.PivotRoot(newRoot, oldRoot); err != nil {
		return fmt.Errorf("pivot_root(%s, %s): %w", newRoot, oldRoot, err)
	}

	// 5. Our cwd is still an inode in the old root; move it inside the new one.
	if err := os.Chdir("/"); err != nil {
		return fmt.Errorf("chdir to new root: %w", err)
	}

	// 6. Detach the host filesystem. MNT_DETACH (a lazy unmount) is what runc
	//    uses too: it succeeds even though the old root's submounts are still
	//    referenced, and the tree goes away as soon as the last reference does.
	if err := syscall.Unmount("/"+oldRootName, syscall.MNT_DETACH); err != nil {
		return fmt.Errorf("detach old root: %w", err)
	}
	if err := os.Remove("/" + oldRootName); err != nil {
		return fmt.Errorf("remove old-root mount point: %w", err)
	}

	return mountProc()
}

// mountProc mounts a fresh procfs at /proc inside the container. Because the
// process is already in a new PID namespace, this procfs is populated with that
// namespace's view -- so `ps` shows only container processes and PID 1 is the
// contained command, not the host's init.
//
// This is also why /proc must be mounted AFTER pivot_root and after the PID
// namespace exists: procfs snapshots the PID namespace of whoever mounts it.
func mountProc() error {
	if err := os.MkdirAll("/proc", 0o555); err != nil {
		return fmt.Errorf("create /proc mount point: %w", err)
	}
	if err := syscall.Mount("proc", "/proc", "proc", 0, ""); err != nil {
		return fmt.Errorf("mount /proc: %w", err)
	}
	return nil
}
