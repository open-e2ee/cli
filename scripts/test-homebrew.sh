#!/usr/bin/env bash
set -euo pipefail

repository_root=$(cd "$(dirname "$0")/.." && pwd)
temp_base=${TMPDIR:-/tmp}
temp_base=${temp_base%/}
test_root=$(mktemp -d "$temp_base/oe-homebrew.XXXXXX")
tap_name="open-e2ee/cli-local-$$"

cleanup() {
  brew uninstall "$tap_name/oe" >/dev/null 2>&1 || true
  brew untap "$tap_name" >/dev/null 2>&1 || true
  if [[ -n "$test_root" && "$test_root" == "$temp_base"/oe-homebrew.* ]]; then
    rm -r "$test_root"
  fi
}
trap cleanup EXIT

brew tap-new --no-git "$tap_name"
tap_root=$(brew --repository "$tap_name")
cp "$repository_root/Formula/oe.rb" "$tap_root/Formula/oe.rb"
brew audit --strict "$tap_name/oe"

tar \
  --exclude=.git \
  --exclude=bin \
  --exclude=dist \
  --exclude=node_modules \
  --exclude=.release \
  -czf "$test_root/oe-0.1.0.tar.gz" \
  -C "$repository_root" .
checksum=$(shasum -a 256 "$test_root/oe-0.1.0.tar.gz" | awk '{print $1}')

# shellcheck disable=SC2016
OE_FORMULA_URL="file://$test_root/oe-0.1.0.tar.gz" \
OE_FORMULA_SHA256="$checksum" \
ruby -pi -e '
  if $_.include?(%q{  head "https://github.com/open-e2ee/cli.git", branch: "main"})
    $_ = "  url \"#{ENV.fetch("OE_FORMULA_URL")}\"\n  version \"0.1.0\"\n  sha256 \"#{ENV.fetch("OE_FORMULA_SHA256")}\"\n"
  end
' "$tap_root/Formula/oe.rb"

HOMEBREW_NO_AUTO_UPDATE=1 brew install "$tap_name/oe"
brew test "$tap_name/oe"
