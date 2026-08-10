#!/bin/sh
set -eu

say() {
    printf '%s\n' "dora installer: $*"
}

fail() {
    printf '%s\n' "dora installer: $*" >&2
    exit 1
}

download() {
    url=$1
    output=$2
    if command -v curl >/dev/null 2>&1; then
        curl --proto '=https' --tlsv1.2 -LsSf "$url" -o "$output"
    elif command -v wget >/dev/null 2>&1; then
        wget -qO "$output" "$url"
    else
        fail "curl or wget is required"
    fi
}

version=${DORA_VERSION:-__DORA_VERSION__}
case "$version" in
    v*) ;;
    *) fail "invalid version $version" ;;
esac
case "$version" in
    *[!A-Za-z0-9._+-]*) fail "invalid version $version" ;;
esac

case "$(uname -s)" in
    Darwin) os=darwin ;;
    Linux) os=linux ;;
    *) fail "unsupported operating system: $(uname -s)" ;;
esac

case "$(uname -m)" in
    x86_64 | amd64) arch=amd64 ;;
    arm64 | aarch64) arch=arm64 ;;
    *) fail "unsupported architecture: $(uname -m)" ;;
esac

if [ -n "${DORA_INSTALL_DIR:-}" ]; then
    install_dir=$DORA_INSTALL_DIR
elif [ -n "${HOME:-}" ]; then
    install_dir=$HOME/.local/bin
else
    fail "HOME is unset; set DORA_INSTALL_DIR"
fi

archive=dora-$os-$arch.tar.gz
release_base=${DORA_RELEASE_BASE_URL:-https://github.com/lgxz/dora/releases/download}
release_url=$release_base/$version

temp_dir=$(mktemp -d "${TMPDIR:-/tmp}/dora-installer.XXXXXX")
temp_target=
temp_marker=
cleanup() {
    rm -rf "$temp_dir"
    if [ -n "$temp_target" ]; then
        rm -f "$temp_target"
    fi
    if [ -n "$temp_marker" ]; then
        rm -f "$temp_marker"
    fi
}
trap cleanup EXIT HUP INT TERM

say "downloading $version for $os/$arch"
download "$release_url/$archive" "$temp_dir/$archive"
download "$release_url/checksums.txt" "$temp_dir/checksums.txt"

expected=$(awk -v name="$archive" '$2 == name || $2 == "*" name { print $1; exit }' "$temp_dir/checksums.txt")
[ -n "$expected" ] || fail "checksum for $archive is missing"

if command -v sha256sum >/dev/null 2>&1; then
    actual=$(sha256sum "$temp_dir/$archive" | awk '{ print $1 }')
elif command -v shasum >/dev/null 2>&1; then
    actual=$(shasum -a 256 "$temp_dir/$archive" | awk '{ print $1 }')
elif command -v openssl >/dev/null 2>&1; then
    actual=$(openssl dgst -sha256 "$temp_dir/$archive" | awk '{ print $NF }')
else
    fail "sha256sum, shasum, or openssl is required"
fi

[ "$actual" = "$expected" ] || fail "checksum verification failed for $archive"

tar -xzf "$temp_dir/$archive" -C "$temp_dir"
[ -f "$temp_dir/dora" ] || fail "archive does not contain dora"

mkdir -p "$install_dir"
install_dir=$(cd "$install_dir" && pwd -P)
temp_target=$install_dir/.dora-installer.$$
temp_marker=$install_dir/.dora-install.$$
cp "$temp_dir/dora" "$temp_target"
chmod 0755 "$temp_target"
printf '%s\n' '{"schema":1,"repository":"lgxz/dora"}' >"$temp_marker"
chmod 0644 "$temp_marker"
mv -f "$temp_target" "$install_dir/dora"
temp_target=
mv -f "$temp_marker" "$install_dir/.dora-install.json"
temp_marker=

say "installed $install_dir/dora"
"$install_dir/dora" --version

case ":${PATH:-}:" in
    *:"$install_dir":*) ;;
    *) say "add $install_dir to PATH to run dora directly" ;;
esac
