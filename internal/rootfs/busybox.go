//go:build linux

package rootfs

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// SetupBusybox lays out a minimal root filesystem at destDir built around a
// single static busybox binary: it copies busybox into <destDir>/bin, creates a
// symlink there for every applet busybox provides (so /bin/sh, /bin/ls, etc.
// all resolve to busybox), and makes the empty mount-point directories a shell
// expects (/proc, /sys, /dev, /tmp, /root).
//
// This is complete scaffolding -- it's pure file I/O, not one of the kernel
// primitives the milestones are about, so it's done for you. It expects a
// `busybox` binary on PATH; the dev container installs busybox-static, and any
// Linux box can `apt-get install busybox-static` / `apk add busybox-static`.
//
// The result is a plain directory you point `minirun run` at -- deliberately not
// an OCI image (no manifest, no layers): unpacking real images is explicitly out
// of scope (see the README's non-goals).
func SetupBusybox(destDir string) error {
	busyboxPath, err := exec.LookPath("busybox")
	if err != nil {
		return fmt.Errorf("busybox not found on PATH (install busybox-static): %w", err)
	}

	for _, dir := range []string{"bin", "proc", "sys", "dev", "tmp", "root", "etc"} {
		if err := os.MkdirAll(filepath.Join(destDir, dir), 0o755); err != nil {
			return fmt.Errorf("mkdir %s: %w", dir, err)
		}
	}

	destBusybox := filepath.Join(destDir, "bin", "busybox")
	if err := copyExecutable(busyboxPath, destBusybox); err != nil {
		return fmt.Errorf("copy busybox: %w", err)
	}

	applets, err := listApplets(busyboxPath)
	if err != nil {
		return fmt.Errorf("list busybox applets: %w", err)
	}
	for _, applet := range applets {
		if applet == "busybox" {
			continue
		}
		link := filepath.Join(destDir, "bin", applet)
		// Relative target so the symlink still resolves after pivot_root, when
		// the new root is "/". Every applet lives alongside busybox in /bin.
		if err := os.Symlink("busybox", link); err != nil && !os.IsExist(err) {
			return fmt.Errorf("symlink %s: %w", applet, err)
		}
	}
	return nil
}

// listApplets asks busybox which commands it can act as.
func listApplets(busyboxPath string) ([]string, error) {
	output, err := exec.Command(busyboxPath, "--list").Output()
	if err != nil {
		return nil, err
	}
	return strings.Fields(string(output)), nil
}

// copyExecutable copies src to dst with owner-executable permissions.
func copyExecutable(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Close()
}
