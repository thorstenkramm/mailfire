#!/usr/bin/env bash
# Run as root in a disposable Debian/Ubuntu or Fedora container.
set -euo pipefail
version="${1:?Expected package version}"
base=https://dl.cloudsmith.io/public/thorstenkramm/mailfire
fingerprint=6479EED42348F9DA69632428B43413AD5FA82F43

if command -v apt-get >/dev/null; then
  export DEBIAN_FRONTEND=noninteractive
  apt-get update -qq
  apt-get install -y -qq ca-certificates curl gnupg
else
  dnf install -y ca-certificates curl gnupg2
fi
curl -fsSL "$base/gpg.B43413AD5FA82F43.key" -o /tmp/mailfire.key
actual=$(gpg --show-keys --with-colons /tmp/mailfire.key | awk -F: '$1 == "fpr" {print $10; exit}')
test "$actual" = "$fingerprint"

if command -v apt-get >/dev/null; then
  gpg --batch --yes --dearmor -o /usr/share/keyrings/mailfire.gpg /tmp/mailfire.key
  # Cloudsmith expands any-distro uploads into distribution-specific APT indexes.
  # shellcheck disable=SC1091
  . /etc/os-release
  echo "deb [signed-by=/usr/share/keyrings/mailfire.gpg] $base/deb/$ID $VERSION_CODENAME main" > /etc/apt/sources.list.d/mailfire.list
  apt-get update -qq
  apt-get install -y "mailfire=$version"
else
  rpm --import /tmp/mailfire.key
  cat > /etc/yum.repos.d/mailfire.repo <<EOF
[mailfire]
name=Mailfire
baseurl=$base/rpm/any-distro/any-version/\$basearch
enabled=1
gpgcheck=1
repo_gpgcheck=1
gpgkey=file:///tmp/mailfire.key
sslverify=1
skip_if_unavailable=0
EOF
  dnf install -y "mailfire-$version"
fi
mailfire version | grep -F "$version"
mailfire --help >/dev/null
test -f /usr/share/man/man1/mailfire.1
test -f /usr/share/bash-completion/completions/mailfire
test -f /usr/share/zsh/site-functions/_mailfire
test -f /usr/share/licenses/mailfire/LICENSE

# Package removal must preserve user-owned state.
mkdir -p "$HOME/.config/mailfire"
echo smoke-test > "$HOME/.config/mailfire/preserve-test"
if command -v apt-get >/dev/null; then
  apt-get remove -y mailfire
else
  dnf remove -y mailfire
fi
test "$(cat "$HOME/.config/mailfire/preserve-test")" = smoke-test
