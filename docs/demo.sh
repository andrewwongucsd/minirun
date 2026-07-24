#!/usr/bin/env bash
# Driver for the README demo recording. Runs REAL minirun commands inside the
# privileged dev container; the terminal session is captured to docs/demo.svg by
# termtosvg (see docs/record-demo.sh). Nothing here is faked — every line of
# output is produced by the runtime.
set -euo pipefail

cyan=$'\033[38;5;79m'; dim=$'\033[38;5;244m'; off=$'\033[0m'
say() { printf '%s$%s %s\n' "$cyan" "$off" "$1"; sleep 0.7; }
note() { printf '%s# %s%s\n' "$dim" "$1" "$off"; sleep 0.5; }

# minirun is expected on PATH already (the recorder builds it first) so this
# script stays a pure demonstration with no build noise.
command -v minirun >/dev/null || { echo "minirun not on PATH — build it first"; exit 1; }

note "a container from scratch: namespaces + cgroups v2 + pivot_root"
sleep 0.6

say "minirun setup-rootfs ./rootfs"
minirun setup-rootfs ./rootfs
sleep 0.9

say "minirun run --mem=100m --cpu=0.5 ./rootfs -- /bin/sh"
minirun run --mem=100m --cpu=0.5 ./rootfs -- /bin/sh -c '
  printf "  echo \$\$      -> %s\n" "$$"
  printf "  hostname     -> %s\n" "$(hostname)"
  printf "  ls /         -> %s\n" "$(ls / | tr "\n" " ")"
  printf "  ps | wc -l   -> %s processes (own procfs)\n" "$(ps | wc -l)"
'
sleep 0.4
note "PID 1, own hostname, only the rootfs, its own process table"
sleep 1.1

say "minirun run --mem=32m ./rootfs -- sh -c '<allocate past the limit>'"
set +e
minirun run --mem=32m ./rootfs -- /bin/sh -c 'x=$(yes aaaaaaaa | head -c 1048576); while :; do x="$x$x"; done'
code=$?
set -e
printf '  -> exit %s ' "$code"
note "the kernel OOM-killed it at memory.max (128 + SIGKILL = 137)"
sleep 1.4
