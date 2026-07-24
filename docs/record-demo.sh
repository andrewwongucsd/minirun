#!/usr/bin/env bash
# Regenerate docs/demo.gif from docs/demo.sh, from the macOS host.
#
# Records a REAL minirun session inside the privileged dev container (the only
# place the runtime can run) with asciinema, then renders a GIF with agg. The
# `demo` GitHub workflow does the same on a runner; this is the local path.
# Re-run after changing demo.sh.
#
#   ./docs/record-demo.sh
set -euo pipefail
cd "$(dirname "$0")/.."

docker build -f Dockerfile.dev -t minirun-dev . >/dev/null

docker run --rm --privileged --cgroupns=host -v "$PWD:/workspace" -w /workspace minirun-dev \
  bash -c '
    set -e
    apt-get update -qq && apt-get install -y -qq asciinema >/dev/null
    curl -sSL -o /usr/local/bin/agg \
      https://github.com/asciinema/agg/releases/latest/download/agg-x86_64-unknown-linux-gnu
    chmod +x /usr/local/bin/agg
    go build -o /usr/local/bin/minirun ./cmd/minirun
    minirun setup-rootfs ./rootfs
    asciinema rec docs/demo.cast --overwrite --cols 84 --rows 22 --command "bash docs/demo.sh"
    agg --theme monokai --font-size 22 --speed 1.3 --idle-time-limit 2 docs/demo.cast docs/demo.gif
  '
echo "wrote docs/demo.gif"
