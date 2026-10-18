# Cloudsmith publication (maintainers)

Moved from the public Linux package guide; not published on the documentation site.

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
