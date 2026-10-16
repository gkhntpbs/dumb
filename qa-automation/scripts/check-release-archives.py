#!/usr/bin/env python3
"""Verify actual archives, checksum entries and embedded version/key.

Verify the manifest signature separately before using this packaging check.
Published assets require --version; snapshots default to their own metadata.
Pass the expected public (not private) key as an argument.
"""
import argparse
import hashlib
import json
import pathlib
import plistlib
import struct
import stat
import subprocess
import tempfile
import tarfile
import zipfile

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument("directory", nargs="?", default="dist")
parser.add_argument("public_key")
parser.add_argument("--require-apps", action="store_true", help="require both native macOS application archives")
parser.add_argument("--version", help="expected published version, without a v prefix")
parser.add_argument("--commit", help="expected immutable source commit")
args = parser.parse_args()
root = pathlib.Path(args.directory)
key = args.public_key
assert key, "expected public key required"
version = args.version or json.loads((root / "metadata.json").read_text())["version"]
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
                assert all((i.external_attr >> 16) & 0o170000 in (0, stat.S_IFREG) for i in archive.infolist()), (name, "nonregular ZIP entry")
                data = archive.read(binary)
                if product == "worker":
                    assert "dumb-tray.exe" in names, name
        else:
            with tarfile.open(path) as archive:
                names = archive.getnames()
                assert all(i.isfile() for i in archive.getmembers()), (name, "nonregular tar entry")
                data = archive.extractfile(binary).read()
        assert len(names) == len(set(names)), (name, "duplicate archive entries")
        assert set(names) == {"LICENSE", "README.md", binary} | ({"dumb-tray.exe"} if system == "windows" and product == "worker" else set()), (name, names)
        with tempfile.NamedTemporaryFile(dir=root, prefix=".inspect-") as binary_file:
            binary_file.write(data)
            binary_file.flush()
            info = json.loads(subprocess.check_output(["go", "version", "-m", "-json", binary_file.name]))
        settings = {item["Key"]: item["Value"] for item in info["Settings"]}
        # -trimpath omits ldflags from build info. Check the version/key bytes
        # together with the immutable clean source revision and platform info.
        assert version.encode() in data, (name, "embedded version missing")
        assert info["Path"] == "github.com/gkhntpbs/dumb/cmd/dumb-" + product, (name, "unexpected main package")
        assert settings["GOOS"] == system and settings["GOARCH"] == arch and settings["CGO_ENABLED"] == "0", (name, "platform build mismatch")
        if args.commit:
            assert settings.get("vcs.revision") == args.commit, (name, "source revision mismatch")
            assert settings.get("vcs.modified") == "false", (name, "dirty source build")
        if product == "worker":
            assert key.encode() in data, (name, "release public key missing")
        print(name, "checksum, members and embedded release metadata verified")
for arch, cpu in ((("arm64", 0x0100000C), ("amd64", 0x01000007)) if args.require_apps else ()):
    name = f"dumb-worker-app_{version}_darwin_{arch}.zip"
    path = root / name
    assert hashlib.sha256(path.read_bytes()).hexdigest() == sums[name], name
    prefix = "DUMB Worker.app/Contents/"
    with zipfile.ZipFile(path) as archive:
        names = archive.namelist()
        assert len(names) == len(set(names)), (name, "duplicate archive entries")
        assert all(not pathlib.PurePosixPath(item).is_absolute() and ".." not in pathlib.PurePosixPath(item).parts for item in names), (name, "unsafe path")
        assert all((item.external_attr >> 16) & 0o170000 in (0, stat.S_IFREG, stat.S_IFDIR) for item in archive.infolist()), (name, "nonregular ZIP entry")
        worker = archive.read(prefix + "MacOS/dumb-worker")
        helper = archive.read(prefix + "Resources/DUMBMenu.app/Contents/MacOS/dumb-menu")
        assert struct.unpack_from("<II", helper) == (0xFEEDFACF, cpu), (name, "helper architecture mismatch")
        for plist_path in ("Info.plist", "Resources/DUMBMenu.app/Contents/Info.plist"):
            plist = plistlib.loads(archive.read(prefix + plist_path))
            assert plist["CFBundleVersion"] == version.split("-")[0], (name, "bundle version mismatch")
        assert archive.read(prefix + "Resources/AppIcon.icns"), (name, "missing icon")
    with tempfile.NamedTemporaryFile(dir=root, prefix=".inspect-") as binary_file:
        binary_file.write(worker)
        binary_file.flush()
        info = json.loads(subprocess.check_output(["go", "version", "-m", "-json", binary_file.name]))
    settings = {item["Key"]: item["Value"] for item in info["Settings"]}
    assert info["Path"] == "github.com/gkhntpbs/dumb/cmd/dumb-worker", name
    assert settings["GOOS"] == "darwin" and settings["GOARCH"] == arch and settings["CGO_ENABLED"] == "0", name
    assert version.encode() in worker and key.encode() in worker, (name, "embedded release metadata missing")
    if args.commit:
        assert settings.get("vcs.revision") == args.commit and settings.get("vcs.modified") == "false", (name, "source revision mismatch")
    print(name, "checksum, bundle, helper architecture and embedded release metadata verified")
assert len(sums) == (12 if args.require_apps else 10), "unexpected release checksum entries"
