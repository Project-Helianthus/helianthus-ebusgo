#!/usr/bin/env python3
"""Reproduce the passive-capture smoke command rename from a pre-rename tree.

The command internals under cmd/aa29-smoke carried the AA31/aa31 work-item
prefix on every identifier, import alias, file and the internal package. This
script renames them to behavior names and changes nothing else:

* identifier tokens in code and in comments are renamed through one table: the
  prefix is dropped, with the listed exceptions where the plain name would
  collide or read wrongly; test function names (TestAA31...) are unchanged;
* import aliases that only carried the prefix are dropped;
* string literals are unchanged except the internal package import path and
  the test constants that pin file names;
* the two tests that required the prefix on every declared identifier are
  removed, together with their helpers and the imports only they used;
* the files and the internal package directory are moved, gofmt is applied and
  one CHANGELOG entry is added.

It runs offline, needs only Python 3 and gofmt, and refuses to run unless the
tree is in the pre-rename state. Usage: rename_smoke_capture_names.py [ROOT]
"""

import argparse
import re
import shutil
import subprocess
import sys
from pathlib import Path

COMMAND = Path("cmd/aa29-smoke")
OLD_PACKAGE_DIR = COMMAND / "internal/aa31capture"
NEW_PACKAGE_DIR = COMMAND / "internal/capture"

# Old path -> new path, both relative to the repository root.
FILE_MOVES = [
    (COMMAND / "aa31_smoke_linux_arm64.go", COMMAND / "smoke_linux_arm64.go"),
    (COMMAND / "aa31_smoke_linux_arm64_test.go", COMMAND / "smoke_linux_arm64_test.go"),
    (COMMAND / "aa31_smoke_unsupported.go", COMMAND / "smoke_unsupported.go"),
    (OLD_PACKAGE_DIR / "aa31_capture.go", NEW_PACKAGE_DIR / "capture.go"),
    (OLD_PACKAGE_DIR / "aa31_capture_test.go", NEW_PACKAGE_DIR / "capture_test.go"),
    (OLD_PACKAGE_DIR / "aa31_mapping_unix.go", NEW_PACKAGE_DIR / "mapping_unix.go"),
    (OLD_PACKAGE_DIR / "aa31_mapping_unix_test.go", NEW_PACKAGE_DIR / "mapping_unix_test.go"),
]

# Identifier namespaces: a renamed name must be unique inside its package.
PACKAGES = {
    "main": [old for old, _ in FILE_MOVES[:3]],
    "capture": [old for old, _ in FILE_MOVES[3:]],
}

# Tests (and their helpers) that only enforced the prefix; the rename removes
# what they check, so they go with it.
REMOVED_FUNCS = {
    COMMAND / "aa31_smoke_linux_arm64_test.go": [
        "aa31CheckPrefix",
        "TestAA31EveryDeclaredIdentifierIsPrefixed",
    ],
    OLD_PACKAGE_DIR / "aa31_capture_test.go": [
        "aa31CheckPrefix",
        "aa31CheckSourcePrefixes",
        "TestAA31EveryDeclaredIdentifierIsPrefixed",
    ],
}

# Import lines used only by the removed functions.
REMOVED_LINES = {
    OLD_PACKAGE_DIR / "aa31_capture_test.go": [
        '\taa31ast "go/ast"\n',
        '\taa31parser "go/parser"\n',
        '\taa31token "go/token"\n',
    ],
}

# Exact source substitutions applied before tokenizing (comments only).
SOURCE_SUBS = {
    COMMAND / "aa31_smoke_linux_arm64.go": [
        (" //nolint:staticcheck // ST1012: AA31 prefix is mandatory", ""),
        ("// aa31Window is the AA29 window port", "// readOnlyWindow is the AA29 window port"),
        ("// aa31Mapper maps the owner-named window file.", "// fileMapper maps the owner-named window file."),
    ],
    OLD_PACKAGE_DIR / "aa31_capture.go": [
        ("// The AA31 prefix is mandatory for every identifier in this ticket (enforced by\n"
         "// TestAA31EveryDeclaredIdentifierIsPrefixed), which conflicts with the ErrFoo\n"
         "// naming form that ST1012 expects.\n",
         "// ErrProfile and ErrLimits classify a refused profile document and refused\n"
         "// capture limits.\n"),
        (" //nolint:staticcheck // ST1012: AA31 prefix is mandatory", ""),
        (" //nolint:staticcheck // ST1012: AA31 prefix is mandatory", ""),
    ],
    OLD_PACKAGE_DIR / "aa31_mapping_unix.go": [
        (" //nolint:staticcheck // ST1012: AA31 prefix is mandatory", ""),
        ("// AA31MapReadOnly opens aa31Path read-only and synchronous, maps aa31Length\n"
         "// bytes at the page-aligned file offset aa31Offset, and closes the descriptor.\n",
         "// MapReadOnly opens the named file read-only and synchronous, maps length\n"
         "// bytes at the page-aligned file offset, and closes the descriptor.\n"),
    ],
}

# String literals that follow the rename. Every other literal is unchanged.
STRING_SUBS = {
    '"github.com/Project-Helianthus/helianthus-ebusgo/cmd/aa29-smoke/internal/aa31capture"':
        '"github.com/Project-Helianthus/helianthus-ebusgo/cmd/aa29-smoke/internal/capture"',
    '"aa31_smoke_linux_arm64.go"': '"smoke_linux_arm64.go"',
    '"aa31_smoke_linux_arm64_test.go"': '"smoke_linux_arm64_test.go"',
    '"aa31_smoke_unsupported.go"': '"smoke_unsupported.go"',
}

# Names where dropping the prefix would collide or read wrongly.
EXCEPTIONS = {
    "aa31Capture": "runCapture",  # the package is now named capture
    "aa31Main": "run",            # main is taken by the entry point
    "aa31Case": "tc",             # case is a keyword
}

# The window and mapper types share their token with locals and fields that
# hold one; only type positions take the type name.
TYPE_NAMES = {
    "aa31Window": "readOnlyWindow",
    "aa31Mapper": "fileMapper",
}
TYPE_POSITIONS = {
    (COMMAND / "aa31_smoke_linux_arm64.go", "aa31Window"): 9,
    (COMMAND / "aa31_smoke_linux_arm64.go", "aa31Mapper"): 4,
    (COMMAND / "aa31_smoke_linux_arm64_test.go", "aa31Mapper"): 2,
}

KEYWORDS = set("""break case chan const continue default defer else fallthrough
for func go goto if import interface map package range return select struct
switch type var""".split())
PREDECLARED = set("""any append bool byte cap clear close complex complex64
complex128 comparable copy delete error false float32 float64 imag int int8
int16 int32 int64 iota len make max min new nil panic print println real
recover rune string true uint uint8 uint16 uint32 uint64 uintptr""".split())

TOKEN = re.compile(
    r"(?P<comment>//[^\n]*|/\*.*?\*/)"
    r"|(?P<string>`[^`]*`|\"(?:\\.|[^\"\\\n])*\"|'(?:\\.|[^'\\\n])*')"
    r"|(?P<ident>[A-Za-z_][A-Za-z0-9_]*)"
    r"|(?P<space>\s+)"
    r"|(?P<other>.)",
    re.S,
)
PREFIXED = re.compile(r"(?i)^aa31")
COMMENT_WORD = re.compile(r"\b(?:aa31|AA31)[A-Za-z0-9_]*")

CHANGELOG_MARKER = "## Unreleased\n\n"
CHANGELOG_ENTRY = """\
- Rename the passive-capture smoke command internals under `cmd/aa29-smoke`
  to behavior names. The internal package `internal/aa31capture` becomes
  `internal/capture`. Files: `aa31_smoke_linux_arm64.go` →
  `smoke_linux_arm64.go`, `aa31_smoke_linux_arm64_test.go` →
  `smoke_linux_arm64_test.go`, `aa31_smoke_unsupported.go` →
  `smoke_unsupported.go`, and in the package `aa31_capture.go` →
  `capture.go`, `aa31_capture_test.go` → `capture_test.go`,
  `aa31_mapping_unix.go` → `mapping_unix.go`, `aa31_mapping_unix_test.go` →
  `mapping_unix_test.go`. Identifiers drop the `AA31`/`aa31` prefix (for
  example `AA31DecodeProfile` → `DecodeProfile`, `AA31ErrProfile` →
  `ErrProfile`, `aa31ParseOptions` → `parseOptions`), import aliases that only
  carried the prefix are removed, and the exceptions are `aa31Capture` →
  `runCapture`, `aa31Main` → `run`, the window type `aa31Window` →
  `readOnlyWindow`, the mapper type `aa31Mapper` → `fileMapper` and
  `aa31Case` → `tc`. Test function names are unchanged; the two tests that
  required the prefix on every identifier are removed. The command directory,
  the `aa29_local_gpio` build tag, the flags, the report JSON keys and every
  usage and error text are unchanged.
  `scripts/rename_smoke_capture_names.py` reproduces the change.

"""


def fail(message):
    raise SystemExit(f"rename_smoke_capture_names: {message}")


def lower_first_word(rest):
    """Lower the leading word of a CamelCase tail, keeping Go initialisms whole."""
    run = re.match(r"[A-Z]+", rest).group()
    if len(run) == 1 or len(run) == len(rest):
        return run.lower() + rest[len(run):]
    if rest[len(run)].islower():
        return run[:-1].lower() + rest[len(run) - 1:]
    return run.lower() + rest[len(run):]


def new_name(old):
    if old in EXCEPTIONS:
        return EXCEPTIONS[old]
    if old.startswith("TestAA31"):
        return old
    if old.startswith("AA31") and old[4:5].isupper():
        return old[4:]
    if old.startswith("aa31") and old[4:5].isupper():
        return lower_first_word(old[4:])
    if old.startswith("aa31") and old[4:5].islower():
        return old[4:]
    fail(f"no rule for identifier {old!r}")


def tokens(text):
    return [(m.lastgroup, m.group()) for m in TOKEN.finditer(text)]


def remove_func(text, name, path):
    match = re.search(rf"^func {re.escape(name)}\(", text, re.M)
    if match is None or len(re.findall(rf"^func {re.escape(name)}\(", text, re.M)) != 1:
        fail(f"{path}: expected one func {name}")
    start = match.start()
    lines_before = text[:start].split("\n")[:-1]
    while lines_before and lines_before[-1].startswith("//"):
        start -= len(lines_before.pop()) + 1
    end = text.index("\n}\n", match.start()) + len("\n}\n")
    if text[start - 2:start] == "\n\n":
        start -= 1
    return text[:start] + text[end:]


def substitute(text, old, new, path):
    if text.count(old) < 1:
        fail(f"{path}: missing {old!r}")
    return text.replace(old, new, 1)


def rename_text(text, path, type_counts):
    out = []
    toks = tokens(text)
    significant = [i for i, (kind, _) in enumerate(toks) if kind not in ("space", "comment")]
    position = {index: order for order, index in enumerate(significant)}
    for index, (kind, value) in enumerate(toks):
        if kind == "ident" and PREFIXED.match(value):
            replacement = new_name(value)
            if value in TYPE_NAMES:
                order = position[index]
                before = toks[significant[order - 1]][1] if order else ""
                after = toks[significant[order + 1]][1] if order + 1 < len(significant) else ""
                if before in ("type", "*") or (before == "&" and after == "{"):
                    replacement = TYPE_NAMES[value]
                    type_counts[(path, value)] = type_counts.get((path, value), 0) + 1
            out.append(replacement)
        elif kind == "comment":
            out.append(COMMENT_WORD.sub(lambda m: new_name(m.group()), value))
        elif kind == "string" and value in STRING_SUBS:
            out.append(STRING_SUBS[value])
        else:
            out.append(value)
    return drop_redundant_aliases("".join(out))


def drop_redundant_aliases(text):
    def drop(match):
        alias, path = match.group(2), match.group(3)
        if alias == path.rsplit("/", 1)[-1]:
            return f'{match.group(1)}"{path}"'
        return match.group(0)

    text = re.sub(r'^(\t)([A-Za-z_][A-Za-z0-9_]*) "([^"]+)"$', drop, text, flags=re.M)
    return re.sub(r'^(import )([A-Za-z_][A-Za-z0-9_]*) "([^"]+)"$', drop, text, flags=re.M)


def check_names(sources):
    """Every package's renamed names are valid, distinct and collision free."""
    mapping = {}
    for package, paths in PACKAGES.items():
        renamed, untouched, aliases = {}, set(), set()
        for path in paths:
            previous = ""
            for kind, value in tokens(sources[path]):
                if kind in ("space", "comment"):
                    continue
                if kind == "ident" and PREFIXED.match(value):
                    renamed[value] = new_name(value)
                    if value[4:5].islower():
                        aliases.add(new_name(value))
                elif kind == "ident" and previous != ".":
                    # Selectors name members of other types and cannot collide.
                    untouched.add(value)
                previous = value
        for old, target in list(renamed.items()):
            if old in TYPE_NAMES:
                renamed[old + " (type)"] = TYPE_NAMES[old]
        seen = {}
        for old, target in renamed.items():
            if target in KEYWORDS or target in PREDECLARED:
                fail(f"{package}: {old} -> {target} is reserved")
            if target != old and target in untouched:
                fail(f"{package}: {old} -> {target} collides with an existing name")
            if target in aliases and old[4:5].isupper():
                fail(f"{package}: {old} -> {target} shadows an imported package")
            if target in seen and seen[target] != old:
                fail(f"{package}: {seen[target]} and {old} both become {target}")
            seen[target] = old
            mapping[old] = target
    return mapping


def verify(root):
    remaining = []
    for _, new in FILE_MOVES:
        for kind, value in tokens((root / new).read_text()):
            if kind == "ident" and PREFIXED.match(value) and not value.startswith("TestAA31"):
                remaining.append(f"{new}: identifier {value}")
            if kind == "comment" and re.search(r"(?i)\baa31", value):
                remaining.append(f"{new}: comment {value}")
    if remaining:
        fail("prefixed names remain:\n" + "\n".join(remaining))


def gofmt_binary():
    found = shutil.which("gofmt")
    if found:
        return found
    goroot = subprocess.run(["go", "env", "GOROOT"], check=True, capture_output=True, text=True).stdout.strip()
    candidate = Path(goroot) / "bin" / "gofmt"
    if not candidate.exists():
        fail("gofmt not found")
    return str(candidate)


def rename(root, print_map):
    for old, new in FILE_MOVES:
        if not (root / old).is_file():
            fail(f"missing {old}; the tree is not in the pre-rename state")
        if (root / new).exists():
            fail(f"{new} already exists")
    if (root / NEW_PACKAGE_DIR).exists():
        fail(f"{NEW_PACKAGE_DIR} already exists")
    changelog = root / "CHANGELOG.md"
    log = changelog.read_text()
    if log.count(CHANGELOG_MARKER) != 1:
        fail("CHANGELOG.md has no single Unreleased section")

    sources = {}
    for old, _ in FILE_MOVES:
        text = (root / old).read_text()
        for name in REMOVED_FUNCS.get(old, []):
            text = remove_func(text, name, old)
        for line in REMOVED_LINES.get(old, []):
            if text.count(line) != 1:
                fail(f"{old}: expected one {line!r}")
            text = text.replace(line, "", 1)
        for before, after in SOURCE_SUBS.get(old, []):
            text = substitute(text, before, after, old)
        sources[old] = text
    mapping = check_names(sources)

    type_counts = {}
    results = {new: rename_text(sources[old], old, type_counts) for old, new in FILE_MOVES}
    if type_counts != TYPE_POSITIONS:
        fail(f"type positions {type_counts} differ from {TYPE_POSITIONS}")

    (root / OLD_PACKAGE_DIR).rename(root / NEW_PACKAGE_DIR)
    for old, new in FILE_MOVES:
        moved = root / NEW_PACKAGE_DIR / old.name if old.parent == OLD_PACKAGE_DIR else root / old
        moved.rename(root / new)
        (root / new).write_text(results[new])
    subprocess.run([gofmt_binary(), "-w"] + [str(root / new) for _, new in FILE_MOVES], check=True)
    changelog.write_text(log.replace(CHANGELOG_MARKER, CHANGELOG_MARKER + CHANGELOG_ENTRY, 1))
    verify(root)

    if print_map:
        for old in sorted(mapping):
            if mapping[old] != old and " " not in old:
                print(f"{old} -> {mapping[old]}")
        for old, target in sorted(TYPE_NAMES.items()):
            print(f"{old} (type positions) -> {target}")
    exported = sum(1 for old in mapping if old.startswith("AA31") and " " not in old)
    unexported = sum(1 for old in mapping if old.startswith("aa31") and " " not in old)
    print(f"renamed {exported} exported and {unexported} unexported identifiers in {len(FILE_MOVES)} files",
          file=sys.stderr)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("root", nargs="?", type=Path, default=Path(__file__).resolve().parent.parent)
    parser.add_argument("--print-map", action="store_true", help="print the identifier map after renaming")
    arguments = parser.parse_args()
    rename(arguments.root.resolve(), arguments.print_map)
