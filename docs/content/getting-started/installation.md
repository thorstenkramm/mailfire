---
title: Installation
description: Install Mailfire with Homebrew, from the Linux package repositories, from a release download, or from source.
weight: 10
icon: download
---

Every [GitHub release](https://github.com/thorstenkramm/mailfire/releases) includes binaries for Linux and macOS
(amd64 and arm64), Windows (amd64), DEB and RPM packages, and a `checksums.txt` file.

## macOS: Homebrew

Install on Apple Silicon or Intel Macs using [Homebrew](https://brew.sh):

```shell
brew tap thorstenkramm/tap
brew trust --cask thorstenkramm/tap/mailfire
brew install --cask mailfire
mailfire version
```

The cask includes the binary, man page, and Bash/Zsh completions. To update:

```shell
brew update
brew upgrade --cask mailfire
```

The macOS binary is not Apple-notarized. If Gatekeeper blocks it and you trust this release, remove the quarantine
attribute from the installed binary, then retry:

```shell
xattr -d com.apple.quarantine "$(realpath "$(command -v mailfire)")"
mailfire version
```

## Linux: package repositories

Install and update Mailfire through the public Cloudsmith repository (amd64 and arm64). No account or API key is
needed. The repository is signed, and the setup restricts it to the `mailfire` package only.

- [Debian / Ubuntu (APT)](linux-packages.md#debian--ubuntu)
- [Fedora / RPM (DNF)](linux-packages.md#fedora--rpm)

The [Linux package guide](linux-packages.md) covers signing keys, repository configuration, updates, and removal.

## Manual download

Download the archive or package for your platform from the
[release page](https://github.com/thorstenkramm/mailfire/releases/latest) and verify its checksum against
`checksums.txt`. On Linux, use `sha256sum -c --ignore-missing checksums.txt`; on macOS, use `shasum -a 256 <file>`
and compare the result with the corresponding entry.

For **Debian / Ubuntu**, install the downloaded DEB package (replace `<version>` and select your architecture):

```shell
sudo dpkg -i mailfire_<version>_linux_amd64.deb
```

For **Fedora / RHEL**, use the RPM package instead:

```shell
sudo rpm -i mailfire_<version>_linux_amd64.rpm
```

For a **binary archive**, extract it and put `mailfire` (or `mailfire.exe` on Windows) in a directory on your `PATH`.
The macOS Gatekeeper workaround above also applies to downloaded binaries.

## Build from source

With Go 1.27.2 or newer installed, build from a source checkout:

```shell
git clone https://github.com/thorstenkramm/mailfire.git
cd mailfire
make build
```

## Man page and shell completions

Packages and the Homebrew cask install the man page and Bash/Zsh completions. Archives include them under `man/` and
`completions/`. To install completions manually, source `completions/mailfire.bash` from `~/.bashrc`, or save
`completions/mailfire.zsh` as `_mailfire` in a directory on your Zsh `fpath`. In a source checkout, these files are
under `packaging/completions/`, and the man page is `docs/mailfire.1`. It is also available as
[a web page](../reference/man-page.md).

Next: [Quick start](quick-start.md).
