# Linux package repositories

Mailfire's public [Cloudsmith repository](https://cloudsmith.io/~thorstenkramm/repos/mailfire/packages/)
provides DEB and RPM packages for amd64/x86_64 and arm64/aarch64.
Downloads require no account or API key. Packages use Cloudsmith's
`any-distro/any-version` layout; installation checks cover Debian 13,
Ubuntu 24.04, and Fedora 44 on both architectures.

## Signing key

Download the repository key and check its fingerprint before importing it:

```shell
curl -fsSL https://dl.cloudsmith.io/public/thorstenkramm/mailfire/gpg.B43413AD5FA82F43.key -o mailfire.key
gpg --show-keys --with-fingerprint mailfire.key
```

Expected fingerprint: `6479 EED4 2348 F9DA 6963 2428 B434 13AD 5FA8 2F43`.
Install `curl`, CA certificates and GnuPG first if needed.

## Debian / Ubuntu

```shell
gpg --dearmor < mailfire.key | sudo tee /usr/share/keyrings/mailfire.gpg > /dev/null
. /etc/os-release
echo "deb [signed-by=/usr/share/keyrings/mailfire.gpg] https://dl.cloudsmith.io/public/thorstenkramm/mailfire/deb/$ID $VERSION_CODENAME main" | sudo tee /etc/apt/sources.list.d/mailfire.list
sudo tee /etc/apt/preferences.d/mailfire > /dev/null <<'EOF'
Package: mailfire
Pin: release o=cloudsmith/thorstenkramm/mailfire
Pin-Priority: 500

Package: *
Pin: release o=cloudsmith/thorstenkramm/mailfire
Pin-Priority: -1
EOF
sudo apt update
apt-cache policy mailfire
sudo apt install mailfire
mailfire version
```

The preferences allow only the exact package `mailfire` from this repository;
all other package names receive priority `-1` and cannot be selected from it.
The rules match the signed Release `Origin: cloudsmith/thorstenkramm/mailfire`,
not the shared Cloudsmith hostname. They work with both traditional `.list`
and deb822 `.sources` files (see the [README](../README.md#debian--ubuntu-cloudsmith-apt-repository)).
Keep `signed-by` / `Signed-By` enabled as well: key trust and package selection
are separate controls. `apt-cache policy mailfire` should show the Cloudsmith
version at priority `500`; `apt-cache policy` shows the repository-wide `-1` default.

Upgrade with `sudo apt update && sudo apt install --only-upgrade mailfire`.
Remove with `sudo apt remove mailfire`. To stop using the repository, remove
`/etc/apt/sources.list.d/mailfire.list` and `/usr/share/keyrings/mailfire.gpg`,
then remove `/etc/apt/preferences.d/mailfire` and run `sudo apt update`.
If you used the README's setup, remove `mailfire.sources` (or `mailfire.list`)
and `/etc/apt/keyrings/mailfire.gpg` instead of the source/key paths above.

## Fedora / RPM

After checking the key above:

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
