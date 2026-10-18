# Homebrew distribution (macOS)

The public tap is `thorstenkramm/homebrew-tap`. The `mailfire` cask selects the
Intel or Apple Silicon archive from GitHub, verifies SHA-256, and installs the
binary, man page, and bash/zsh completions. Linux is not supported by this cask.

```shell
brew install --cask thorstenkramm/tap/mailfire
mailfire version
brew upgrade --cask thorstenkramm/tap/mailfire
```

Remove with `brew uninstall --cask mailfire`. The cask has no zap rules and does
not remove configuration, credentials, logs, or deferred messages.

## Maintainers

`.github/workflows/homebrew.yml` validates the candidate on native Apple Silicon
and Intel macOS runners before publishing it. It runs cask style/audit checks,
installation with default quarantine behavior, version/help, installed-file checks,
and uninstallation. Apple Developer signing/notarization is not configured;
the workflow does not disable Gatekeeper or remove quarantine attributes.
Cross-version upgrade verification awaits the next real release.

The release workflow calls this workflow after stable GitHub publication.
For initial publication or retries:

```shell
gh workflow run homebrew.yml -f tag=v0.1.0
```

`HOMEBREW_TAP_GITHUB_TOKEN` is a fine-grained token scoped to the tap repository
with Contents read/write. Only the publish job receives it. Rotate it by updating
the Mailfire Actions secret before revoking the previous token.

`packaging/homebrew/release.py` uses the published archives and checksums without
rebuilding. Publishing is serialized; retries accept identical casks, reject
changed contents for an existing version, and reject downgrades. GitHub's content
SHA provides an additional concurrent-update check. Failures leave the existing
tap and GitHub release intact; fix the cause and dispatch the same tag again.
