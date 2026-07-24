#!/usr/bin/env bash
# Regenerate docs/demo.svg from docs/demo.sh, from the macOS host.
#
# Records a REAL minirun session inside the privileged dev container (the only
# place the runtime can run) with termtosvg, producing a self-contained animated
# SVG that GitHub renders inline in the README. Re-run after changing demo.sh.
#
#   ./docs/record-demo.sh
set -euo pipefail
cd "$(dirname "$0")/.."

docker build -f Dockerfile.dev -t minirun-dev . >/dev/null

docker run --rm --privileged --cgroupns=host -v "$PWD:/workspace" -w /workspace minirun-dev \
  bash -c '
    set -e
    pip install --quiet --break-system-packages termtosvg 2>/dev/null || pip install --quiet termtosvg
    termtosvg docs/demo.svg \
      -c "bash docs/demo.sh" \
      -t window_frame \
      -g 84x22 \
      -m 24 \
      -M 1600
  '
echo "wrote docs/demo.svg"
