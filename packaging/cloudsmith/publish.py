#!/usr/bin/env python3
"""Publish verified GitHub release packages; requires gh and cloudsmith-cli 1.29.0."""

import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import tempfile
import time
import urllib.parse
import urllib.request

REPOSITORY = "thorstenkramm/mailfire"
API = f"https://api.cloudsmith.io/packages/{REPOSITORY}/"


def api(path=""):
    request = urllib.request.Request(
        API + path, headers={"X-Api-Key": os.environ["CLOUDSMITH_API_KEY"]}
    )
    with urllib.request.urlopen(request, timeout=60) as response:
        return json.load(response)


def release_version(tag):
    if not re.fullmatch(r"v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)", tag):
        raise ValueError("Expected a stable release tag: vMAJOR.MINOR.PATCH")
    return tag[1:]


def verify_files(directory, version):
    checksums = {}
    for line in (directory / "checksums.txt").read_text().splitlines():
        digest, name = line.split()
        if name in checksums or not re.fullmatch(r"[a-f0-9]{64}", digest):
            raise ValueError("Invalid or duplicate checksum entry")
        checksums[name] = digest
    verified = []
    for fmt in ("deb", "rpm"):
        for arch in ("amd64", "arm64"):
            file = directory / f"mailfire_{version}_linux_{arch}.{fmt}"
            digest = hashlib.sha256(file.read_bytes()).hexdigest()
            if checksums.get(file.name) != digest:
                raise ValueError(f"Checksum mismatch: {file.name}")
            verified.append((file, digest))
    return verified


def check_existing(package, digest):
    # Cloudsmith signs RPMs, changing their archive hash. Record the verified
    # input hash as provenance at upload time; never replace existing packages.
    if f"github-sha256-{digest}" not in package.get("tags", {}).get("info", []):
        raise ValueError(f"Existing package has different or unknown provenance: {package['filename']}")


def wait_ready(identifier):
    deadline = time.monotonic() + 900
    while time.monotonic() < deadline:
        package = api(f"{identifier}/")
        if package["is_sync_failed"]:
            raise RuntimeError(f"Cloudsmith synchronization failed: {identifier}")
        if package["is_sync_completed"] and package["is_downloadable"]:
            if package.get("is_quarantined"):
                raise RuntimeError(f"Package is quarantined: {identifier}")
            return
        time.sleep(10)
    raise TimeoutError(f"Cloudsmith synchronization timed out: {identifier}")


def publish(tag):
    version = release_version(tag)
    if not os.environ.get("CLOUDSMITH_API_KEY"):
        raise ValueError("CLOUDSMITH_API_KEY is required")
    release = json.loads(subprocess.check_output(
        ["gh", "release", "view", tag, "--repo", REPOSITORY,
         "--json", "isDraft,isPrerelease,tagName"], text=True
    ))
    if release["isDraft"] or release["isPrerelease"] or release["tagName"] != tag:
        raise ValueError("Only published stable GitHub releases can be distributed")
    with tempfile.TemporaryDirectory(prefix="mailfire-cloudsmith-") as directory:
        directory = Path(directory)
        subprocess.run(
            ["gh", "release", "download", tag, "--repo", REPOSITORY,
             "--dir", str(directory), "--pattern", "*.deb", "--pattern", "*.rpm",
             "--pattern", "checksums.txt"], check=True
        )
        verified = verify_files(directory, version)  # Validate all before any upload.
        for file, digest in verified:
            query = urllib.parse.urlencode({"query": f"filename:{file.name}", "page_size": 100})
            existing = [p for p in api("?" + query) if p["filename"] == file.name]
            if len(existing) > 1:
                raise ValueError(f"Duplicate packages already exist: {file.name}")
            if existing:
                check_existing(existing[0], digest)
                wait_ready(existing[0]["slug_perm"])
                print(f"Already published: {file.name}", flush=True)
                continue
            subprocess.run(
                ["cloudsmith", "push", file.suffix[1:],
                 f"{REPOSITORY}/any-distro/any-version", str(file),
                 "--no-republish", "--tags", f"github-sha256-{digest}"],
                check=True, timeout=1200
            )
            print(f"Published: {file.name}", flush=True)
    summary = f"Cloudsmith: {tag} — four DEB/RPM packages synchronized.\n"
    print(summary)
    if os.environ.get("GITHUB_STEP_SUMMARY"):
        with open(os.environ["GITHUB_STEP_SUMMARY"], "a") as output:
            output.write(summary)


if __name__ == "__main__":
    if len(sys.argv) != 2:
        sys.exit("Usage: publish.py vMAJOR.MINOR.PATCH")
    publish(sys.argv[1])
