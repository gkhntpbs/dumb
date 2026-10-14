#!/usr/bin/env python3
"""Verify the actual snapshot archives, checksum entries and embedded version/key.

This is packaging evidence, not a published signature/update acceptance test.
Pass the expected public (not private) key as an argument.
"""
import hashlib
import json
import pathlib
import subprocess
import sys
import tarfile
import zipfile

root = pathlib.Path(sys.argv[1] if len(sys.argv) > 1 else "dist")
key = sys.argv[2] if len(sys.argv) > 2 else ""
assert key, "expected public key required"
version = json.loads((root / "metadata.json").read_text())["version"]
sums = dict(line.split()[::-1] for line in (root / "checksums.txt").read_text().splitlines())
platforms = [("darwin", "amd64"), ("darwin", "arm64"), ("linux", "amd64"), ("linux", "arm64"), ("windows", "amd64")]
for product in ("worker", "server"):
    for system, arch in platforms:
        extension = ".zip" if system == "windows" else ".tar.gz"
        name = f"dumb-{product}_{version}_{system}_{arch}{extension}"
        path = root / name
        assert hashlib.sha256(path.read_bytes()).hexdigest() == sums[name], name
        binary = f"dumb-{product}" + (".exe" if system == "windows" else "")
        if system == "windows":
            with zipfile.ZipFile(path) as archive:
                names = archive.namelist()
                data = archive.read(binary)
                if product == "worker":
                    assert "dumb-tray.exe" in names, name
        else:
            with tarfile.open(path) as archive:
                names = archive.getnames()
                data = archive.extractfile(binary).read()
        assert set(names) == {"LICENSE", "README.md", binary} | ({"dumb-tray.exe"} if system == "windows" and product == "worker" else set()), (name, names)
        assert version.encode() in data, (name, "version missing")
        if product == "worker":
            assert key.encode() in data, (name, "release public key missing")
        print(name, "checksum, members and embedded release metadata verified")
assert len(sums) == 10, "unexpected release checksum entries"
