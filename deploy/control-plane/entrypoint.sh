#!/bin/sh
set -eu

if [ "$#" -gt 0 ]; then
    exec su-exec flowops:flowops "$@"
fi

mount_path="${RAILWAY_VOLUME_MOUNT_PATH:-/var/lib/flowops}"
case "$mount_path" in
    /|*//*|*/../*|*/..|*/./*|*/.)
        echo "invalid reconciliation volume mount path" >&2
        exit 1
        ;;
    /*) ;;
    *)
        echo "reconciliation volume mount path must be absolute" >&2
        exit 1
        ;;
esac

journal_path="${FLOWOPS_RECONCILIATION_JOURNAL:-$mount_path/reconciliation.log}"
case "$journal_path" in
    *//*|*/../*|*/..|*/./*|*/.)
        echo "invalid reconciliation journal path" >&2
        exit 1
        ;;
    "$mount_path"/*) ;;
    *)
        echo "reconciliation journal must be inside the mounted volume" >&2
        exit 1
        ;;
esac
journal_dir=$(dirname "$journal_path")
install -d -m 0700 -o flowops -g flowops "$journal_dir"
export FLOWOPS_RECONCILIATION_JOURNAL="$journal_path"

exec su-exec flowops:flowops /flowops/control-plane-api
