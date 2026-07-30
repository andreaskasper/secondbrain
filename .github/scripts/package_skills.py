#!/usr/bin/env python3
"""Package every skill directory under skills/ into skills/<name>.skill.

A .skill file is a zip archive of the skill directory, so unpacking one yields
the directory back. The archive is built deterministically: entries are sorted,
timestamps are fixed and permissions are normalised, which means an unchanged
skill hashes to an unchanged file. Without that, every CI run would produce a
different archive and the repository would fill up with commits that change
nothing.
"""

import hashlib
import os
import re
import sys
import zipfile

SKILLS_DIR = "skills"

# A fixed timestamp, not the file's own. Zip stores mtimes, and a checkout
# gives every file the time of the checkout, so real timestamps would make the
# output differ on every run.
FIXED_TIME = (1980, 1, 1, 0, 0, 0)

# Things that belong to the working copy rather than to the skill.
EXCLUDE_DIRS = {".git", ".github", "__pycache__", ".pytest_cache", ".ruff_cache", "node_modules"}
EXCLUDE_FILES = {".DS_Store", "Thumbs.db", ".gitkeep"}
EXCLUDE_SUFFIXES = (".pyc", ".swp", ".skill")

NAME_RE = re.compile(r"^[a-z0-9][a-z0-9-]*$")


def fail(msg):
    print(f"::error::{msg}")
    sys.exit(1)


def read_frontmatter(path):
    """Pull the YAML frontmatter out of SKILL.md without requiring PyYAML.

    Only the two fields that matter are parsed. A full YAML parse would be
    stricter, but it would also make this script depend on something that has
    to be installed first, for no gain at this level of checking.
    """
    with open(path, encoding="utf-8") as fh:
        text = fh.read()
    if not text.startswith("---"):
        return None
    end = text.find("\n---", 3)
    if end < 0:
        return None
    block = text[text.index("\n", 3) + 1:end]

    fields, key = {}, None
    for line in block.splitlines():
        if not line.strip() or line.lstrip().startswith("#"):
            continue
        if line[0] not in " \t" and ":" in line:
            key, _, value = line.partition(":")
            key = key.strip()
            fields[key] = value.strip()
        elif key:                       # folded or continued value
            fields[key] += " " + line.strip()
    return fields


def collect(root):
    """Every file in the skill, as (absolute path, path inside the archive)."""
    out = []
    for dirpath, dirnames, filenames in os.walk(root):
        dirnames[:] = sorted(d for d in dirnames if d not in EXCLUDE_DIRS and not d.startswith("."))
        for name in sorted(filenames):
            if name in EXCLUDE_FILES or name.endswith(EXCLUDE_SUFFIXES) or name.startswith("."):
                continue
            abs_path = os.path.join(dirpath, name)
            # The archive keeps the skill directory as its top level, so
            # unpacking abc.skill gives you abc/.
            arc = os.path.relpath(abs_path, os.path.dirname(root)).replace(os.sep, "/")
            out.append((abs_path, arc))
    return sorted(out, key=lambda p: p[1])


def build(name, root, dest):
    files = collect(root)
    if not files:
        fail(f"{name}: the directory holds no files")

    with zipfile.ZipFile(dest, "w", zipfile.ZIP_DEFLATED, compresslevel=9) as zf:
        for abs_path, arc in files:
            info = zipfile.ZipInfo(arc, date_time=FIXED_TIME)
            info.compress_type = zipfile.ZIP_DEFLATED
            # 0644, and the high bits that mark this as a regular file.
            info.external_attr = (0o100644 & 0xFFFF) << 16
            info.create_system = 3      # Unix, so the mode above is honoured
            with open(abs_path, "rb") as fh:
                zf.writestr(info, fh.read())
    return files


def main():
    if not os.path.isdir(SKILLS_DIR):
        fail(f"{SKILLS_DIR}/ does not exist")

    names = sorted(
        d for d in os.listdir(SKILLS_DIR)
        if os.path.isdir(os.path.join(SKILLS_DIR, d)) and not d.startswith(".")
    )
    if not names:
        fail(f"{SKILLS_DIR}/ holds no skill directories")

    built = []
    for name in names:
        root = os.path.join(SKILLS_DIR, name)
        dest = os.path.join(SKILLS_DIR, f"{name}.skill")

        if not NAME_RE.match(name):
            fail(f"{name}: a skill directory must be lowercase letters, digits and hyphens")

        skill_md = os.path.join(root, "SKILL.md")
        if not os.path.isfile(skill_md):
            fail(f"{name}: no SKILL.md - a skill without one cannot be installed")

        fields = read_frontmatter(skill_md)
        if fields is None:
            fail(f"{name}/SKILL.md: no YAML frontmatter block at the top of the file")
        for required in ("name", "description"):
            if not fields.get(required):
                fail(f"{name}/SKILL.md: frontmatter is missing '{required}'")
        declared = fields["name"].strip().strip("\"'")
        if declared != name:
            fail(f"{name}/SKILL.md: frontmatter says name: {declared!r}, "
                 f"but the directory is {name!r}. They have to agree.")
        # The description is what a model matches on when deciding whether to
        # load the skill. A thin one is a skill that never triggers.
        if len(fields["description"]) < 40:
            fail(f"{name}/SKILL.md: the description is too short to route on")

        before = None
        if os.path.exists(dest):
            with open(dest, "rb") as fh:
                before = hashlib.sha256(fh.read()).hexdigest()

        files = build(name, root, dest)

        with open(dest, "rb") as fh:
            after = hashlib.sha256(fh.read()).hexdigest()
        size = os.path.getsize(dest)
        state = "unchanged" if before == after else ("updated" if before else "new")
        print(f"{dest:<48} {len(files):>3} files  {size:>7} bytes  {state}")
        built.append((name, state))

    changed = [n for n, s in built if s != "unchanged"]
    summary = ", ".join(changed) if changed else "no change"
    step_out = os.environ.get("GITHUB_OUTPUT")
    if step_out:
        with open(step_out, "a", encoding="utf-8") as fh:
            fh.write(f"summary={summary}\n")
            fh.write(f"changed={'true' if changed else 'false'}\n")
    print(f"\n{len(built)} skill(s) packaged; changed: {summary}")


if __name__ == "__main__":
    main()
