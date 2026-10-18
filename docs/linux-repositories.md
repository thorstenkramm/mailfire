# Linux package repositories

Mailfire's public [Cloudsmith repository](https://cloudsmith.io/~thorstenkramm/repos/mailfire/packages/)
provides DEB and RPM packages for amd64/x86_64 and arm64/aarch64.
Downloads require no account or API key. Packages use Cloudsmith's
`any-distro/any-version` layout; installation checks cover Debian 13,
Ubuntu 24.04, and Fedora 44 on both architectures.

## Signing key

Repository key: `https://dl.cloudsmith.io/public/thorstenkramm/mailfire/gpg.B43413AD5FA82F43.key`

Expected fingerprint: `6479 EED4 2348 F9DA 6963 2428 B434 13AD 5FA8 2F43`.
Do not install a key that does not match.

## Debian / Ubuntu

Already added this repository earlier and `apt update` reports
`NO_PUBKEY B43413AD5FA82F43`? Do not run the block below; see
[Manual setup and recovery](#manual-setup-and-recovery).

### Install

```shell
(
  set -e
  sudo apt update
  sudo apt install -y ca-certificates curl
  sudo tee /etc/apt/preferences.d/mailfire > /dev/null <<'EOF'
Package: mailfire
Pin: release o=cloudsmith/thorstenkramm/mailfire
Pin-Priority: 500

Package: *
Pin: release o=cloudsmith/thorstenkramm/mailfire
Pin-Priority: -1
EOF
  curl -fsSL https://dl.cloudsmith.io/public/thorstenkramm/mailfire/setup.deb.sh -o mailfire-setup.sh
  sudo bash mailfire-setup.sh
  sudo apt install mailfire
  mailfire version
)
```

The block stops at the first failing command. It first restricts the repository
to the single package `mailfire`; every other package name from it gets priority
`-1` and can never be installed. Then Cloudsmith's official setup script installs
`gnupg` if needed, adds the signing key, adds the repository with `signed-by`
(so APT accepts only that key for it), and runs `apt update`.

The script is saved to `mailfire-setup.sh` before it runs, so a failed download
stops the block before anything runs as root. To read the script first, run the
`curl` line on its own, open `mailfire-setup.sh`, then run the rest.
`apt-cache policy mailfire` should show the Cloudsmith version at priority `500`.

### Updates and removal

Upgrade with `sudo apt update && sudo apt install --only-upgrade mailfire`.
Remove the package with `sudo apt remove mailfire`.

To stop using the repository, delete its files and run `sudo apt update`:

- Setup script: `/etc/apt/sources.list.d/thorstenkramm-mailfire.list` and
  `/usr/share/keyrings/thorstenkramm-mailfire-archive-keyring.gpg`
- Manual setup: `/etc/apt/sources.list.d/mailfire.list` (or `mailfire.sources`)
  and `/usr/share/keyrings/mailfire.gpg`; very old README instructions used
  `/etc/apt/keyrings/mailfire.gpg` instead
- Both: `/etc/apt/preferences.d/mailfire`

### Manual setup and recovery

Use either the setup script or the manual setup, never both. Two entries for the
same repository with different keyrings make `apt update` fail.

<details>
<summary>Fixing <code>NO_PUBKEY B43413AD5FA82F43</code> after an earlier manual setup</summary>

This error means the Mailfire source exists but the keyring it points to is
missing or empty (older versions of these instructions could cause this). Do not
disable signature checks. Reinstall the key; this does not need a working
`apt update`.

First check which keyring your source expects:

```shell
grep -i signed-by /etc/apt/sources.list.d/mailfire*
```

This prints `signed-by=…` (`mailfire.list`) or `Signed-By: …` (`mailfire.sources`).
The block below installs the key to `/usr/share/keyrings/mailfire.gpg`. If the
path shown is `/etc/apt/keyrings/mailfire.gpg` (used by older instructions),
change the first line to `keyring=/etc/apt/keyrings/mailfire.gpg`. The key must
be installed at exactly the path the source names.

```shell
keyring=/usr/share/keyrings/mailfire.gpg
(
  set -e
  cd "$(mktemp -d)"
  curl -fsSL https://dl.cloudsmith.io/public/thorstenkramm/mailfire/gpg.B43413AD5FA82F43.key -o mailfire.key
  fpr=$(gpg --show-keys --with-colons mailfire.key | awk -F: '$1 == "fpr" {print $10}')
  [ "$fpr" = 6479EED42348F9DA69632428B43413AD5FA82F43 ] || { echo "Wrong key fingerprint: $fpr" >&2; exit 1; }
  gpg --dearmor -o mailfire.gpg mailfire.key
  sudo install -d -m 0755 "$(dirname "$keyring")"
  sudo install -m 0644 mailfire.gpg "$keyring"
)
sudo apt update
```

If `curl` or `gpg` is missing, first run `sudo apt install -y ca-certificates curl gnupg`;
this uses the package lists already on the system. If
`/etc/apt/preferences.d/mailfire` does not exist, create it as shown in the
[Install](#install) block before installing Mailfire.

</details>

<details>
<summary>Manual setup without the setup script</summary>

This does the same as the setup script, but downloads the key yourself, checks the
[fingerprint](#signing-key), and uses the file names `mailfire.list` and
`mailfire.gpg`. It stops before adding the repository if the key does not match.

```shell
sudo apt update
sudo apt install -y ca-certificates curl gnupg
sudo tee /etc/apt/preferences.d/mailfire > /dev/null <<'EOF'
Package: mailfire
Pin: release o=cloudsmith/thorstenkramm/mailfire
Pin-Priority: 500

Package: *
Pin: release o=cloudsmith/thorstenkramm/mailfire
Pin-Priority: -1
EOF
(
  set -e
  cd "$(mktemp -d)"
  curl -fsSL https://dl.cloudsmith.io/public/thorstenkramm/mailfire/gpg.B43413AD5FA82F43.key -o mailfire.key
  fpr=$(gpg --show-keys --with-colons mailfire.key | awk -F: '$1 == "fpr" {print $10}')
  [ "$fpr" = 6479EED42348F9DA69632428B43413AD5FA82F43 ] || { echo "Wrong key fingerprint: $fpr" >&2; exit 1; }
  gpg --dearmor -o mailfire.gpg mailfire.key
  sudo install -m 0644 mailfire.gpg /usr/share/keyrings/mailfire.gpg
  . /etc/os-release
  echo "deb [signed-by=/usr/share/keyrings/mailfire.gpg] https://dl.cloudsmith.io/public/thorstenkramm/mailfire/deb/$ID $VERSION_CODENAME main" | sudo tee /etc/apt/sources.list.d/mailfire.list > /dev/null
)
sudo apt update
sudo apt install mailfire
mailfire version
```

If it prints `Wrong key fingerprint`, neither the key nor the repository was
added; do not work around the check.

To use a deb822 `mailfire.sources` file instead of `mailfire.list`, replace the
`echo "deb ..."` line above with the following (and use only one of the two files):

```shell
sudo tee /etc/apt/sources.list.d/mailfire.sources > /dev/null <<EOF
Types: deb
URIs: https://dl.cloudsmith.io/public/thorstenkramm/mailfire/deb/$ID
Suites: $VERSION_CODENAME
Components: main
Signed-By: /usr/share/keyrings/mailfire.gpg
EOF
```

</details>

## Fedora / RPM

Install the prerequisites if needed, then download the key and check that the
fingerprint matches the [signing key](#signing-key) above:

```shell
sudo dnf install -y ca-certificates curl gnupg2
curl -fsSL https://dl.cloudsmith.io/public/thorstenkramm/mailfire/gpg.B43413AD5FA82F43.key -o mailfire.key
gpg --show-keys --with-fingerprint mailfire.key
```

Only after the fingerprint matches:

```shell
sudo install -m 0644 mailfire.key /etc/pki/rpm-gpg/mailfire.key
sudo rpm --import /etc/pki/rpm-gpg/mailfire.key
sudo tee /etc/yum.repos.d/mailfire.repo > /dev/null <<'EOF'
[mailfire]
name=Mailfire
baseurl=https://dl.cloudsmith.io/public/thorstenkramm/mailfire/rpm/any-distro/any-version/$basearch
enabled=1
includepkgs=mailfire
gpgcheck=1
repo_gpgcheck=1
gpgkey=file:///etc/pki/rpm-gpg/mailfire.key
sslverify=1
skip_if_unavailable=0
EOF
sudo dnf install mailfire
mailfire version
```

`includepkgs=mailfire` allows only that exact package name from this repository;
other names are excluded, including when resolving dependencies. Keep both
`gpgcheck=1` and `repo_gpgcheck=1` to verify packages and repository metadata.
Do not use `--disableexcludes` to bypass the allowlist.

Upgrade with `sudo dnf upgrade mailfire`; remove with `sudo dnf remove mailfire`.
To stop using the repository, remove `/etc/yum.repos.d/mailfire.repo`.

APT pinning and DNF's allowlist govern repository-based package selection. They
do not sandbox Mailfire or restrict manually downloaded packages installed via
`dpkg -i` or `rpm -i`.

Packages do not own your configuration, credentials, logs or deferred messages;
those remain after removal. Older versions remain available: list them with
`apt-cache madison mailfire` or `dnf --showduplicates list mailfire`, then select
the desired version using `apt install mailfire=VERSION --allow-downgrades` or
`dnf downgrade mailfire-VERSION`.

## Maintainers: publication and recovery

- `.github/workflows/release.yml` calls the Cloudsmith workflow after GitHub
  publication. Stable releases only; no separate release event is needed.
- GitHub secret `CLOUDSMITH_API_KEY` authenticates uploads. Rotate it by replacing
  the secret with a new Cloudsmith key before revoking the old one. Local
  `.cloudsmith` is ignored and must never be committed.
- Use the **Cloudsmith** workflow's manual dispatch with an existing stable tag
  to bootstrap a release or retry a partial failure:
  `gh workflow run cloudsmith.yml -f tag=v0.1.0`.
- The publisher downloads all four packages from GitHub and validates them
  against `checksums.txt` before uploading. It does not rebuild packages.
- Cloudsmith signs RPMs, so their served archive hashes differ from GitHub's.
  The verified original hash is recorded in a `github-sha256-…` provenance tag.
  Retries require matching provenance and successful synchronization. Existing
  packages with unknown/different provenance fail; they are never overwritten.
- Publication is serialized. A downstream failure leaves the GitHub release
  intact. Check the failed job, correct the cause, then rerun the same tag.
- Each publication runs signed repository installation, version, packaged-file,
  and uninstall checks on native amd64 and arm64 runners. Upgrade testing between
  different releases remains pending until the next real version is published.
- Fix a bad release with a new patch version, preserving existing tags/assets.
  Key rotation requires updating the documented fingerprint and smoke test.

Local publisher checks: `python3 -m unittest discover -s packaging/cloudsmith -v`.
The CLI dependency is pinned to `cloudsmith-cli==1.29.0`; `gh` is also required.
