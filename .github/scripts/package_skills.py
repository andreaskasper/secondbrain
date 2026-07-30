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

try:
    import yaml
except ImportError:                     # PyYAML is preinstalled on the runners
    yaml = None                         # but the script still works without it

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

# The installer refuses a longer description, and it refuses it at install time
# rather than at build time - which is the worst moment to find out. Checking
# here turns a user-facing failure into a failed CI run.
MAX_DESCRIPTION = 1024
MIN_DESCRIPTION = 40
MAX_NAME = 64


def fail(msg):
    print(f"::error::{msg}")
    sys.exit(1)


def read_frontmatter(path):
    """Pull the YAML frontmatter out of SKILL.md.

    PyYAML is used when it is available, because the folded value it produces
    is exactly what the installer will measure - and a length check against a
    slightly different string is a check that passes here and fails there. The
    hand-rolled fallback keeps the script usable without the dependency; it
    joins folded lines the same way, so it is accurate to within the trailing
    newline.
    """
    with open(path, encoding="utf-8") as fh:
        text = fh.read()
    if not text.startswith("---"):
        return None
    end = text.find("\n---", 3)
    if end < 0:
        return None
    block = text[text.index("\n", 3) + 1:end]

    if yaml is not None:
        try:
            parsed = yaml.safe_load(block)
        except yaml.YAMLError as exc:
            fail(f"{path}: the frontmatter is not valid YAML: {exc}")
        if not isinstance(parsed, dict):
            return None
        return {k: (v if isinstance(v, str) else str(v)) for k, v in parsed.items()}

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
        if len(declared) > MAX_NAME:
            fail(f"{name}/SKILL.md: the name is longer than {MAX_NAME} characters")

        # The description is what a model matches on when deciding whether to
        # load the skill. A thin one is a skill that never triggers - and one
        # over the limit is a skill that will not install at all.
        described = fields["description"].strip()
        if len(described) < MIN_DESCRIPTION:
            fail(f"{name}/SKILL.md: the description is too short to route on")
        if len(described) > MAX_DESCRIPTION:
            fail(f"{name}/SKILL.md: the description is {len(described)} characters, "
                 f"and the limit is {MAX_DESCRIPTION}. Drop the weakest trigger "
                 f"phrases first - they are the least load-bearing part of it.")

        before = None
        if os.path.exists(dest):
            with open(dest, "rb") as fh:
                before = hashlib.sha256(fh.read()).hexdigest()

        files = build(name, root, dest)

        with open(dest, "rb") as fh:
            after = hashlib.sha256(fh.read()).hexdigest()
        size = os.path.getsize(dest)
        state = "unchanged" if before == after else ("updated" if before else "new")
        print(f"{dest:<40} {len(files):>3} files  {size:>7} bytes  "
              f"desc {len(described):>4}/{MAX_DESCRIPTION}  {state}")
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
