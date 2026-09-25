"""Offline release candidate checks. Never print secret values or modify Git."""
from pathlib import Path, PurePosixPath
import argparse
import re
import subprocess
import sys
import zipfile

ROOT = Path(__file__).resolve().parents[1]
SECRET_PATTERNS = (
    rb"-----BEGIN (?:RSA |EC |OPENSSH )?PRIVATE KEY-----",
    rb"\bsk-(?:proj-)?[A-Za-z0-9_-]{24,}",
    rb"\b(?:ghp|github_pat)_[A-Za-z0-9_]{30,}",
)


def forbidden(name):
    p = PurePosixPath(name.lower())
    return (
        p.is_absolute() or ".." in p.parts or "\\" in name
        or any(part in {".git", "runtime", "node_modules", "__pycache__", "aq_dic", "aqtk1_win", "aqk2k_win"}
               or part.startswith((".venv", "venv")) or part.endswith(".egg-info") for part in p.parts)
        or (p.name.startswith(".env") and p.name != ".env.example")
        or p.suffix in {".dll", ".lib", ".exe", ".bin", ".dic", ".onnx", ".gguf", ".safetensors", ".wav", ".pyc", ".key", ".pem", ".pfx"}
        or name.startswith(("dist/", "sdk/typescript/dist/", "sdk/python/dist/", "sdk/python/build/"))
    )


def git_files(*args):
    return subprocess.check_output(["git", "-C", str(ROOT), *args], text=True).split("\0")[:-1]


def check_content(name, data):
    errors = []
    if forbidden(name):
        errors.append(f"Forbidden release path: {name}")
    if len(data) > 1024 * 1024 or b"\0" in data:
        errors.append(f"Unexpected binary/large source file: {name}")
    if any(re.search(pattern, data) for pattern in SECRET_PATTERNS):
        errors.append(f"Credential signature in {name} (value redacted)")
    return errors


def candidate_files():
    # Includes new documentation for review before a commit, excludes ignored local assets.
    return sorted(set(git_files("ls-files", "--cached", "--others", "--exclude-standard", "-z")))


def check_candidate():
    errors = []
    files = candidate_files()
    for name in files:
        p = ROOT / name
        if p.is_symlink() or not p.resolve().is_relative_to(ROOT):
            errors.append(f"Source symlink/outside root: {name}")
        elif not p.is_file():
            errors.append(f"Missing candidate file (stage intentional removal): {name}")
        else:
            errors.extend(check_content(name, p.read_bytes()))
    return files, errors


def check_links(files):
    errors = []
    allowed = set(files)
    for name in files:
        if not name.endswith(".md"):
            continue
        content = (ROOT / name).read_text(encoding="utf-8-sig")
        # Check local Markdown paths; remote URLs/anchors need separate review.
        for target in re.findall(r"\]\(([^)]+)\)", content):
            target = target.split("#", 1)[0]
            if not target or re.match(r"[a-zA-Z]+:", target):
                continue
            resolved = ((ROOT / name).parent / target).resolve()
            if not resolved.is_relative_to(ROOT) or resolved.relative_to(ROOT).as_posix() not in allowed:
                errors.append(f"Missing/non-source documentation target: {name} -> {target}")
        if re.search(r"C:[/\\]source[/\\]yukkuri", content, re.I):
            errors.append(f"Developer-specific path: {name}")
        if "???" in content:
            errors.append(f"Possible encoding loss: {name}")
    return errors


def check_contract_docs():
    errors = []
    source = "\n".join((ROOT / name).read_text(encoding="utf-8") for name in
                       ["cmd/engine/main.go", "internal/providers/stt/whispercpp/config.go"])
    variables = set(re.findall(r'"((?:OPENAI|STT|TURN|INTERRUPTION|BACKCHANNEL|SPECULATION|API)_[A-Z_]+)"', source))
    example = (ROOT / ".env.example").read_text(encoding="utf-8")
    declared = set(re.findall(r"^([A-Z_]+)=", example, re.M))
    if variables != declared:
        errors.append(f"Environment example drift: missing={sorted(variables-declared)}, extra={sorted(declared-variables)}")
    for language in ["", ".ja"]:
        config = (ROOT / f"docs/configuration{language}.md").read_text(encoding="utf-8")
        for variable in variables:
            if f"`{variable}`" not in config:
                errors.append(f"Missing configuration documentation: {language or 'en'} {variable}")
    # Commands must match, ignoring explanatory comments outside actual commands.
    for name in ["README", "docs/quickstart"]:
        blocks = []
        for language in ["", ".ja"]:
            content = (ROOT / f"{name}{language}.md").read_text(encoding="utf-8")
            code = re.findall(r"```(?:powershell|ts|python)\n(.*?)```", content, re.S)
            blocks.append([[line for line in block.splitlines() if not line.lstrip().startswith("#")] for block in code])
        if blocks[0] != blocks[1]:
            errors.append(f"EN/JA executable example drift: {name}")
    for name, fields in [("configuration", None), ("performance", None), ("realtime-protocol", 3)]:
        tables = []
        for language in ["", ".ja"]:
            content = (ROOT / f"docs/{name}{language}.md").read_text(encoding="utf-8")
            if name == "configuration":
                rows = [line for line in content.splitlines() if line.startswith("| `")]
            elif name == "performance":
                rows = [line for line in content.splitlines() if re.match(r"\| [0-9]+ \|", line)]
            else:
                rows = ["|".join(line.split("|")[1:fields+1]) for line in content.splitlines()
                        if line.startswith("| ") and re.search(r": `[a-z_]+\.", line)]
            tables.append(rows)
        if tables[0] != tables[1]:
            errors.append(f"EN/JA technical table drift: {name}")
    return errors


def check_revision(ref):
    errors = []
    for name in git_files("ls-tree", "-r", "--name-only", "-z", ref):
        data = subprocess.check_output(["git", "-C", str(ROOT), "show", f"{ref}:{name}"])
        errors.extend(check_content(name, data))
    return errors


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--ref", help="Audit a committed tree as well as the working candidate")
    parser.add_argument("--archive", type=Path, help="Write a working-candidate source zip (no Git history, no binaries)")
    args = parser.parse_args()
    files, errors = check_candidate()
    errors.extend(check_links(files))
    errors.extend(check_contract_docs())
    if args.ref:
        errors.extend(check_revision(args.ref))
    if errors:
        print("\n".join(errors), file=sys.stderr)
        return 1
    if args.archive:
        args.archive.parent.mkdir(parents=True, exist_ok=True)
        # Exclusive create prevents replacing an existing artifact accidentally.
        with zipfile.ZipFile(args.archive, "x", zipfile.ZIP_DEFLATED) as archive:
            for name in files:
                archive.write(ROOT / name, "yukkuri-realtime-engine/" + name)
        with zipfile.ZipFile(args.archive) as archive:
            for member in archive.namelist():
                name = member.removeprefix("yukkuri-realtime-engine/")
                if check_content(name, archive.read(member)):
                    raise RuntimeError(f"Artifact verification failed: {name}")
        print(f"Verified candidate source archive: {args.archive} ({len(files)} files)")
    print(f"Release candidate paths, credential signatures and local documentation links passed ({len(files)} files).")
    print("Signature scanning is not proof of absence of all secrets. Git history requires a separate review.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
