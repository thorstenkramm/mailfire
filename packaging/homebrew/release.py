#!/usr/bin/env python3
"""Generate a cask from verified release assets, optionally publish to the tap."""
import base64
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import tempfile

REPO = "thorstenkramm/mailfire"
TAP = "thorstenkramm/homebrew-tap"


def gh(*args, **kwargs):
    return subprocess.check_output(["gh", *args], text=True, **kwargs)


def main():
    tag = sys.argv[1]
    if not re.fullmatch(r"v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)", tag):
        raise ValueError("Expected a stable vMAJOR.MINOR.PATCH tag")
    version = tag[1:]
    release = json.loads(gh("release", "view", tag, "--repo", REPO,
                            "--json", "isDraft,isPrerelease"))
    if release["isDraft"] or release["isPrerelease"]:
        raise ValueError("Only published stable releases are supported")
    with tempfile.TemporaryDirectory() as directory:
        gh("release", "download", tag, "--repo", REPO, "--dir", directory,
           "--pattern", "*_darwin_*.tar.gz", "--pattern", "checksums.txt")
        root = Path(directory)
        checksums = dict(line.split()[::-1] for line in (root / "checksums.txt").read_text().splitlines())
        hashes = {}
        for arch in ("amd64", "arm64"):
            file = root / f"mailfire_{version}_darwin_{arch}.tar.gz"
            hashes[arch] = hashlib.sha256(file.read_bytes()).hexdigest()
            if hashes[arch] != checksums.get(file.name):
                raise ValueError(f"Checksum mismatch: {file.name}")
    cask = f'''cask "mailfire" do
  arch arm: "arm64", intel: "amd64"

  version "{version}"
  sha256 arm:   "{hashes['arm64']}",
         intel: "{hashes['amd64']}"

  url "https://github.com/thorstenkramm/mailfire/releases/download/v#{{version}}/mailfire_#{{version}}_darwin_#{{arch}}.tar.gz"
  name "Mailfire"
  desc "Send email from the command-line via the Mailgun API"
  homepage "https://github.com/thorstenkramm/mailfire"

  depends_on :macos

  binary "mailfire"
  manpage "man/mailfire.1"
  bash_completion "completions/mailfire.bash", target: "mailfire"
  zsh_completion "completions/mailfire.zsh", target: "_mailfire"
end
'''
    Path(sys.argv[2]).write_text(cask)
    if "--publish" not in sys.argv[3:]:
        return
    env = dict(os.environ, GH_TOKEN=os.environ["HOMEBREW_TAP_GITHUB_TOKEN"])
    endpoint = f"repos/{TAP}/contents/Casks/mailfire.rb"
    # List first so authentication/network failures cannot be mistaken for absence.
    tree = json.loads(gh("api", f"repos/{TAP}/git/trees/main?recursive=1", env=env))
    exists = any(item["path"] == "Casks/mailfire.rb" for item in tree["tree"])
    payload = {"message": f"Update Mailfire to {tag}",
               "content": base64.b64encode(cask.encode()).decode(), "branch": "main"}
    if exists:
        current = json.loads(gh("api", endpoint, env=env))
        text = base64.b64decode(current["content"]).decode()
        match = re.search(r'version "([0-9]+\.[0-9]+\.[0-9]+)"', text)
        if not match:
            raise ValueError("Cannot determine existing cask version")
        old = tuple(map(int, match[1].split('.')))
        new = tuple(map(int, version.split('.')))
        if old > new:
            raise ValueError("Refusing to downgrade the tap")
        if old == new:
            if text != cask:
                raise ValueError("Existing version has different cask contents")
            print("Cask already published")
            return
        payload["sha"] = current["sha"]
    gh("api", "--method", "PUT", endpoint, "--input", "-",
       input=json.dumps(payload), env=env)
    print(f"Published {tag} to {TAP}")


if __name__ == "__main__":
    main()
