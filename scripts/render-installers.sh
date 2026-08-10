#!/bin/sh
set -eu

if [ "$#" -gt 2 ]; then
    echo "usage: $0 VERSION [OUTPUT_DIR]" >&2
    exit 2
fi

version=${1:-}
output_dir=${2:-.release}

if ! printf '%s\n' "$version" | grep -Eq '^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$'; then
    echo "invalid release version: $version" >&2
    exit 2
fi

mkdir -p "$output_dir"
sed "s/__DORA_VERSION__/$version/g" scripts/dora-installer.sh >"$output_dir/dora-installer.sh"
sed "s/__DORA_VERSION__/$version/g" scripts/dora-installer.ps1 >"$output_dir/dora-installer.ps1"
chmod 0755 "$output_dir/dora-installer.sh"
