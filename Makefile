# minirun dev tasks.
#
# minirun is Linux-only (namespaces + cgroups v2), so on macOS you build and run
# it INSIDE a privileged Linux dev container against Docker Desktop's VM. On a
# Linux host you can skip the container and use `go` directly.
#
# The container mounts THIS module directory (not the whole repo) at /workspace,
# so minirun builds standalone from its own go.mod -- it has no dependency on the
# other workspace modules.

IMAGE := minirun-dev
# Run a command in the privileged dev container with this module bind-mounted.
DOCKER_RUN := docker run --rm --privileged --cgroupns=host -v "$(CURDIR):/workspace" -w /workspace $(IMAGE)

.PHONY: dev-image dev-shell docker-build docker-test rootfs vet-typecheck fmt fmt-check clean

dev-image:
	docker build -f Dockerfile.dev -t $(IMAGE) .

dev-shell: dev-image
	docker run --rm -it --privileged --cgroupns=host -v "$(CURDIR):/workspace" -w /workspace $(IMAGE) bash

docker-build: dev-image
	$(DOCKER_RUN) go build ./...

docker-test: dev-image
	$(DOCKER_RUN) go test ./...

# Lay out a static-busybox rootfs at ./rootfs (inside the container, since the
# packaging code is Linux-tagged). Point `minirun run` at it afterwards.
rootfs: dev-image
	$(DOCKER_RUN) go run ./cmd/minirun setup-rootfs ./rootfs

# Type-check the Linux code from the macOS host WITHOUT a container: cross-compile
# the vet pass. Catches undefined syscalls/fields during normal editing. Does not
# run anything -- real execution needs the container (or a Linux host).
vet-typecheck:
	GOOS=linux GOARCH=arm64 go vet ./...

# gofmt works regardless of build tags / GOOS, so it runs fine on the host.
fmt:
	gofmt -w .

fmt-check:
	@test -z "$$(gofmt -l .)" || { echo "gofmt needed:"; gofmt -l .; exit 1; }

clean:
	rm -rf bin rootfs
