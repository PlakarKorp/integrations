#!/usr/bin/env bash

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
WORKSPACE_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
MODULE_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

export E2E_CONNECTOR="native"
export E2E_MODULE_ROOT="$MODULE_ROOT"
export E2E_CREDS_FILE="$SCRIPT_DIR/b2-creds.env"

"$WORKSPACE_ROOT/scripts/e2e-common.sh"
