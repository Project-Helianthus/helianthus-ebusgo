#!/usr/bin/env python3
"""Reproduce the local worker, local GPIO transport and smoke command rename.

The offline local worker (localworker), the local GPIO backend in transport and
the passive-capture smoke command carried the work-item codes AA23, AA24, AA25,
AA26 and AA29 in their file names, their command directory and their
identifiers. This script renames them to behavior names and changes nothing
else:

* files move to behavior names and cmd/aa29-smoke becomes
  cmd/passive-capture-smoke;
* identifier tokens in code, in comments and inside string literals that name
  them are renamed through one rule set:
  - the code is dropped (AA23Registry -> Registry, aa24Lane -> lane,
    ErrAA24Full -> ErrFull, AA25ErrMissingPort -> ErrMissingPort);
  - package-level names of the local GPIO backend in the shared transport
    package take LocalGPIO instead of the code (AA29Backend ->
    LocalGPIOBackend, AA29ErrClosed -> ErrLocalGPIOClosed, aa29Admit ->
    localGPIOAdmit), while its fields, methods and locals drop the code;
  - package-level names that would collide inside localworker after the drop
    take the area of the file that declares them (Plan, IPC, Engine,
    Responder): AA24Config -> IPCConfig, ErrAA23Expired -> ErrPlanExpired;
  - the listed exceptions where the plain name would be a keyword, a
    predeclared name, a legacy terminology word or would read wrongly;
  - a code followed only by a generic word (aa25Self, aa26Self) names one test
    fixture as receiver, local and parameter; it takes a short name derived
    from that fixture type (the initial of its last word, else of the type,
    else its initials) that no function using it already uses: aa25Self -> b
    (bench), aa26Self -> f (responderFixture);
  - test function names (TestAA23...) are unchanged;
* where one token names both a package-level declaration and a field or a
  method, the position decides: selectors on a value, method names, struct
  fields, interface methods and composite-literal keys take the member name;
  the counts of both roles are pinned per file;
* import aliases that only carried the code are dropped;
* string literals are unchanged except the capture package import path, the
  file names tests read, identifier names inside test strings and the leading
  work-item tag of test messages ("aa25 faults %v" -> "faults %v"); every
  production string, the aa29_local_gpio build tag, the aa29.profile.1 digest
  domain, the flags, usage and error texts stay;
* comments name the area instead of the code (the AA25 engine -> the wire
  engine), and command references follow the new command name;
* gofmt is applied, the built-binary ignore rule follows the command name and
  one CHANGELOG entry is added.

It runs offline, needs only Python 3 and gofmt, and refuses to run unless the
tree is in the pre-rename state. Usage: rename_local_worker_names.py [ROOT]
[--print-map]
"""

import argparse
import re
import shutil
import subprocess
import sys
import textwrap
from pathlib import Path

OLD_COMMAND = Path("cmd/aa29-smoke")
NEW_COMMAND = Path("cmd/passive-capture-smoke")
CAPTURE = Path("internal/capture")
MODULE = "github.com/Project-Helianthus/helianthus-ebusgo"

# Old path -> new path, both relative to the repository root, with package.
FILE_MOVES = [
    (Path("localworker/aa23_plans.go"), Path("localworker/plans.go"), "localworker"),
    (Path("localworker/aa23_plans_test.go"), Path("localworker/plans_test.go"), "localworker"),
    (Path("localworker/aa24_ipc.go"), Path("localworker/ipc.go"), "localworker"),
    (Path("localworker/aa24_ipc_test.go"), Path("localworker/ipc_test.go"), "localworker"),
    (Path("localworker/aa25_engine.go"), Path("localworker/engine.go"), "localworker"),
    (Path("localworker/aa25_engine_test.go"), Path("localworker/engine_test.go"), "localworker"),
    (Path("localworker/aa26_responder.go"), Path("localworker/responder.go"), "localworker"),
    (Path("localworker/aa26_responder_test.go"), Path("localworker/responder_test.go"), "localworker"),
    (Path("transport/aa29_local_gpio_linux_arm64.go"), Path("transport/local_gpio_linux_arm64.go"), "transport"),
    (Path("transport/aa29_local_gpio_linux_arm64_test.go"), Path("transport/local_gpio_linux_arm64_test.go"),
     "transport"),
    (OLD_COMMAND / "smoke_linux_arm64.go", NEW_COMMAND / "smoke_linux_arm64.go", "main"),
    (OLD_COMMAND / "smoke_linux_arm64_test.go", NEW_COMMAND / "smoke_linux_arm64_test.go", "main"),
    (OLD_COMMAND / "smoke_unsupported.go", NEW_COMMAND / "smoke_unsupported.go", "main"),
    (OLD_COMMAND / CAPTURE / "capture.go", NEW_COMMAND / CAPTURE / "capture.go", "capture"),
    (OLD_COMMAND / CAPTURE / "capture_test.go", NEW_COMMAND / CAPTURE / "capture_test.go", "capture"),
    (OLD_COMMAND / CAPTURE / "mapping_unix.go", NEW_COMMAND / CAPTURE / "mapping_unix.go", "capture"),
    (OLD_COMMAND / CAPTURE / "mapping_unix_test.go", NEW_COMMAND / CAPTURE / "mapping_unix_test.go", "capture"),
]
DOC = Path("doc.go")
GITIGNORE = Path(".gitignore")

# Directories whose other Go files share a package with a renamed file; their
# package-level names must not be taken by a renamed one.
PACKAGE_DIRS = {
    "localworker": Path("localworker"),
    "transport": Path("transport"),
    "main": OLD_COMMAND,
    "capture": OLD_COMMAND / CAPTURE,
}
IMPORT_NAMES = {"localworker": "localworker", "transport": "transport", "capture": "capture"}

CODE = re.compile(r"(AA|aa)(2[3-6]|29)")
AREA = {"23": "Plan", "24": "IPC", "25": "Engine", "26": "Responder", "29": "LocalGPIO"}

# Package-level names that would collide inside localworker once the code is
# dropped; each takes the area of the file that declares it.
QUALIFIED = """
AA24Config AA25Config AA26Config AA23Limits AA24Limits AA25Stats AA26Stats
ErrAA23Capacity ErrAA24Capacity ErrAA24Clock AA26ErrClock ErrAA23Expired
ErrAA24Expired ErrAA24Identity AA26ErrIdentity AA25ErrInvalidConfig
AA26ErrInvalidConfig ErrAA23InvalidLimits ErrAA24InvalidLimits ErrAA23Oversize
ErrAA24Oversize ErrAA23Revoked ErrAA24Revoked
aa23AddressBroadcast aa25AddressBroadcast aa23CRCPolynomial aa25CRCPolynomial
aa23CRCUpdate aa25CRCUpdate aa23EscapedEscape aa25EscapedEscape aa23EscapedSyn
aa25EscapedSyn aa23SymbolEscape aa25SymbolEscape aa23SymbolSyn aa25SymbolSyn
aa25SymbolAck aa26SymbolAck aa25SymbolNack aa26SymbolNack
aa25FrameCRC aa26FrameCRC aa25FrameData aa26FrameData aa25FrameNN aa26FrameNN
aa25FramePB aa26FramePB aa25FrameSB aa26FrameSB aa25FrameSource aa26FrameSource
aa25FrameTarget aa26FrameTarget aa25FrameUnsynced aa26FrameUnsynced
aa25ParseSource aa26ParseSource aa23Fixture aa26Fixture aa23NewFixture
aa26NewFixture aa23FixtureAddress aa24FixtureAddress aa23FixtureDomain
aa24FixtureDomain aa23FixtureLimits aa24FixtureLimits aa23FixtureStream
aa24FixtureStream aa23FixtureWorker aa24FixtureWorker aa25TestCRC aa26TestCRC
aa25TestConfig aa26TestConfig aa25TestExpand aa26TestExpand aa25TestHz
aa26TestHz aa25TestPB aa26TestPB aa25TestSB aa26TestSB aa25TestSource
aa26TestSource aa23WantErr aa26WantErr aa23WantOK aa26WantOK
""".split()

# Names where the rule would give a keyword, a predeclared name, a legacy
# terminology word or a name that reads wrongly.
EXCEPTIONS = {
    "AA29Open": "OpenLocalGPIO",
    "aa23Any": "anyEntry",
    "aa23Byte": "octet",
    "aa24Byte": "octet",
    "aa23Copy": "copyEntries",
    "aa23Select": "selectAddress",
    "aa24Select": "selectObservation",
    "aa25Select": "eventsOfKind",
    "aa26Select": "selectPlan",
    "aa23Type": "typ",
    "aa24Append": "appendAck",
    "aa24Cap": "payloadCap",
    "aa24Close": "closeWorker",
    "aa26Close": "closeResponder",
    "aa24Len": "count",
    "aa24Max": "limit",
    "aa25Max": "maxTick",
    "aa25Func": "funcDecl",
    "aa26Func": "funcDecl",
    "aa25Import": "importSpec",
    "aa26Import": "importSpec",
    "aa29Import": "importSpec",
    "aa23Case": "tc",
    "aa24Case": "tc",
    "aa25Case": "tc",
    "aa26Case": "tc",
    "aa29Case": "tc",
    "aa26ReasonFromAA23": "reasonFromPlan",
}

# Coded names that carry no meaning of their own once the code is dropped
# ("aa25Self" would leave "self", which ST1006 rejects as a receiver name).
# They name a test fixture as the receiver of its methods and as a local or a
# parameter elsewhere; each takes one short name derived from that fixture type
# (see generic_names).
GENERIC = re.compile(r"^(?:AA|aa)(?:2[3-6]|29)(?:Self|This|Me)$")

KEYWORDS = set("""break case chan const continue default defer else fallthrough
for func go goto if import interface map package range return select struct
switch type var""".split())
PREDECLARED = set("""any append bool byte cap clear close complex complex64
complex128 comparable copy delete error false float32 float64 imag int int8
int16 int32 int64 iota len make max min new nil panic print println real
recover rune string true uint uint8 uint16 uint32 uint64 uintptr""".split())
TERMINOLOGY = re.compile(r"(?i)^(m[a]ster|s[l]ave)$")

TOKEN = re.compile(
    r"(?P<comment>//[^\n]*|/\*.*?\*/)"
    r"|(?P<string>`[^`]*`|\"(?:\\.|[^\"\\\n])*\"|'(?:\\.|[^'\\\n])*')"
    r"|(?P<ident>[A-Za-z_][A-Za-z0-9_]*)"
    r"|(?P<space>\s+)"
    r"|(?P<other>.)",
    re.S,
)
CODED = re.compile(r"^(?:[A-Za-z]*?)(?:AA|aa)(?:2[3-6]|29)")
TEST_FUNC = re.compile(r"^(Test|Benchmark|Fuzz|Example)(AA|aa)(2[3-6]|29)")
# A coded identifier-shaped word inside a comment or a string.
WORD = re.compile(r"\b[A-Za-z]*(?:AA|aa)(?:2[3-6]|29)[A-Za-z][A-Za-z0-9]*\b")
# The same word with the indefinite article in front of it, which follows the
# new name.
ARTICLE_WORD = re.compile(r"\b(an |An |a |A )?(" + WORD.pattern[2:-2] + r")\b")
# The leading work-item tag of a test message.
MESSAGE_TAG = re.compile(r'^(["`])(?:aa|AA)(?:2[3-9]|31) ')

# Comment prose that names an area by its code. Articles first, then the code,
# then the doubled words the plain replacement leaves.
COMMENT_SUBS = {
    Path("localworker/aa23_plans.go"): [
        ("// This file freezes the AA23 registration, plan and pool API for its consumers.",
         "// This file freezes the registration, plan and pool API for its consumers."),
    ],
    Path("localworker/aa25_engine.go"): [
        ("// These exported names keep the AA25 prefix, which conflicts with the ErrFoo\n"
         "// naming form that ST1012 expects.\n",
         "// The engine refuses a configuration, a missing port and a prepared response\n"
         "// with these errors.\n"),
    ],
    Path("localworker/aa26_responder.go"): [
        ("// These exported names keep the AA26 prefix, which conflicts with the ErrFoo\n"
         "// naming form that ST1012 expects.\n",
         "// The responder refuses configuration, identity, authority, wire, clock and\n"
         "// address faults with these errors.\n"),
        ("// This file is the AA26 offline responder path.", "// This file is the offline responder path."),
        ("//   - The only failure action AA23 admits is silence",
         "//   - The only failure action a plan admits is silence"),
    ],
    Path("transport/aa29_local_gpio_linux_arm64.go"): [
        ("// These exported names keep the AA29 prefix, which conflicts with the ErrFoo\n"
         "// naming form that ST1012 expects.\n",
         "// The local GPIO backend refuses profiles, resources, leases and mappings\n"
         "// with these errors.\n"),
    ],
}
NOLINT = " //nolint:staticcheck // ST1012: name keeps its established prefix"
NOLINT_FILES = {
    Path("localworker/aa25_engine.go"): 3,
    Path("localworker/aa26_responder.go"): 8,
    Path("transport/aa29_local_gpio_linux_arm64.go"): 8,
}
ARTICLES = [("an AA25", "a wire engine"), ("an AA29", "a local GPIO"), ("an AA23", "a plan"),
            ("an AA26", "a responder"), ("a AA24", "an IPC")]
AREA_WORDS = {"AA23": "plan", "AA24": "IPC", "AA25": "wire engine", "AA26": "responder",
              "AA29": "local GPIO"}
DOUBLED = [("wire engine engine", "wire engine"), ("plan plan", "plan"),
           ("responder responder", "responder"), ("local GPIO local GPIO", "local GPIO"),
           ("local GPIO GPIO", "local GPIO")]
COMMAND_WORD = re.compile(r"\baa29-smoke\b")
# Test messages still naming the capture errors by their former AA31 names.
PREVIOUS_SLICE_WORD = re.compile(r"\bAA31(?=Err[A-Z])")

CAPTURE_IMPORT_OLD = f'"{MODULE}/{OLD_COMMAND}/{CAPTURE}"'
CAPTURE_IMPORT_NEW = f'"{MODULE}/{NEW_COMMAND}/{CAPTURE}"'

GITIGNORE_OLD = "/aa29-smoke\n"
GITIGNORE_NEW = "/passive-capture-smoke\n"

CHANGELOG_MARKER = "## Unreleased\n\n"


def fail(message):
    raise SystemExit(f"rename_local_worker_names: {message}")


def lower_first_word(rest):
    """Lower the leading word of a CamelCase tail, keeping Go initialisms whole."""
    run = re.match(r"[A-Z]+", rest).group()
    if len(run) == 1 or len(run) == len(rest):
        return run.lower() + rest[len(run):]
    if rest[len(run)].islower():
        return run[:-1].lower() + rest[len(run) - 1:]
    return run.lower() + rest[len(run):]


def split_code(old):
    """Return (head, digits, rest) around the work-item code of a name."""
    match = CODE.search(old)
    if match is None:
        fail(f"no code in {old!r}")
    return old[:match.start()], match.group(2), old[match.end():]


def member_name(old):
    """The name a field, method, local or parameter takes: the code is dropped."""
    if old in EXCEPTIONS:
        return EXCEPTIONS[old]
    if TEST_FUNC.match(old):
        return old
    head, _, rest = split_code(old)
    if head:
        return head + rest
    if old[0] == "A":
        return rest
    if rest[:1].isupper():
        return lower_first_word(rest)
    return rest


def qualified(old, area):
    """Put an area word where the code was, keeping an Err prefix in front."""
    head, _, rest = split_code(old)
    if head == "Err" or (old[0] == "A" and rest.startswith("Err") and rest[3:4].isupper()):
        tail = rest[3:] if not head else rest
        if area == "LocalGPIO" and tail.startswith("GPIO"):
            tail = tail[4:]
        return "Err" + area + tail
    if old[0] == "a":
        lowered = area[0].lower() + area[1:] if area != "IPC" else "ipc"
        if area == "LocalGPIO":
            lowered = "localGPIO"
        return lowered + (rest[:1].upper() + rest[1:])
    if area == "LocalGPIO" and rest.startswith("GPIO"):
        return area + rest[4:]
    return area + rest


def top_name(package, old):
    """The name a package-level declaration takes."""
    if old in EXCEPTIONS:
        return EXCEPTIONS[old]
    _, digits, _ = split_code(old)
    if package == "transport" and digits == "29":
        return qualified(old, AREA["29"])
    if package == "localworker" and old in QUALIFIED:
        return qualified(old, AREA[digits])
    return member_name(old)


def tokens(text):
    return [(m.lastgroup, m.group()) for m in TOKEN.finditer(text)]


def line_ends_after(toks, j):
    """Only spaces and comments follow token j up to the end of its line."""
    for kind, value in toks[j + 1:]:
        if kind == "space" and "\n" in value:
            return True
        if kind not in ("space", "comment"):
            return False
    return True


def function_spans(toks):
    """Token index ranges of the top-level functions of a gofmt-formatted file."""
    spans = []
    for i, (kind, value) in enumerate(toks):
        if kind != "ident" or value != "func":
            continue
        if i and not (toks[i - 1][0] == "space" and toks[i - 1][1].endswith("\n")):
            continue
        depth = 0
        for j in range(i + 1, len(toks)):
            if toks[j] == ("other", "{"):
                depth += 1
            elif toks[j] == ("other", "}"):
                depth -= 1
                if depth == 0 and line_ends_after(toks, j):
                    spans.append((i, j))
                    break
        else:
            fail(f"unterminated function at token {i}")
    return spans


def short_names(type_name):
    """Receiver-style names of a type: its last word's initial, its initial, its initials."""
    words = re.findall(r"[A-Z]+(?![a-z])|[A-Z]?[a-z0-9]+", type_name[:1].upper() + type_name[1:])
    initials = "".join(word[0] for word in words).lower()
    return [words[-1][0].lower(), type_name[0].lower(), initials]


def generic_names(sources):
    """GENERIC name -> the short name of the fixture type it stands for.

    Every top-level function that uses the name is scanned: the receivers and
    the parameters typed with it must name exactly one type, and the short name
    must not be a keyword, a predeclared name or any identifier those functions
    use after the rename (so it neither shadows nor is shadowed)."""
    types, used = {}, {}
    for text, package in sources:
        toks = tokens(text)
        for start, end in function_spans(toks):
            span = [(k, v) for k, v in toks[start:end + 1] if k not in ("space", "comment")]
            names = {v for k, v in span if k == "ident" and GENERIC.match(v)}
            for name in names:
                for n in range(1, len(span) - 2):
                    declared = span[n][1] == name and span[n - 1][1] in ("(", ",")
                    if declared and (span[n + 1][1] == "*" or span[n + 1][0] == "ident"):
                        typed = span[n + 2][1] if span[n + 1][1] == "*" else span[n + 1][1]
                        new_type = top_name(package, typed) if CODED.match(typed) else typed
                        types.setdefault(name, set()).add(new_type)
                bucket = used.setdefault(name, set())
                for kind, value in span:
                    if kind != "ident" or GENERIC.match(value):
                        continue
                    if CODED.match(value) and not TEST_FUNC.match(value):
                        bucket |= {member_name(value), top_name(package, value)}
                    else:
                        bucket.add(value)
    chosen = {}
    for name in sorted(used):
        if len(types.get(name, ())) != 1:
            fail(f"{name} does not name exactly one type: {sorted(types.get(name, ()))}")
        (type_name,) = types[name]
        free = [candidate for candidate in short_names(type_name)
                if candidate not in KEYWORDS | PREDECLARED | used[name]]
        if not free:
            fail(f"{name}: every short name of {type_name} is taken")
        chosen[name] = free[0]
    return chosen


def top_level_names(text):
    """Package-level declared names of one gofmt-formatted Go file."""
    names = set()
    block = None
    for line in text.split("\n"):
        if block:
            if line == ")":
                block = None
                continue
            match = re.match(r"^\t([A-Za-z_][A-Za-z0-9_]*(?:, [A-Za-z_][A-Za-z0-9_]*)*)\b", line)
            if match:
                names.update(match.group(1).split(", "))
            continue
        match = re.match(r"^(type|var|const) \($", line)
        if match:
            block = match.group(1)
            continue
        match = re.match(r"^(?:type|var|const) ([A-Za-z_][A-Za-z0-9_]*(?:, [A-Za-z_][A-Za-z0-9_]*)*)", line)
        if match:
            names.update(match.group(1).split(", "))
            continue
        match = re.match(r"^func ([A-Za-z_][A-Za-z0-9_]*)[\[(]", line)
        if match:
            names.add(match.group(1))
    return names


def import_names(text):
    """Import name -> package key for the module packages a file imports."""
    out = {}
    for alias, path in re.findall(r'^\s*(?:import )?([A-Za-z_][A-Za-z0-9_]*)? ?"([^"]+)"$', text, re.M):
        last = path.rsplit("/", 1)[-1]
        name = alias or last
        for package, import_name in IMPORT_NAMES.items():
            if path.startswith(MODULE + "/") and last == import_name:
                out[name] = package
    return out


class Renamer:
    def __init__(self, tops, generic):
        self.tops = tops  # package -> set of package-level names
        self.generic = generic  # GENERIC name -> short name of its fixture type
        self.mapping = {}  # (package, role, old) -> new
        self.roles = {}  # (file, old, role) -> count

    def name(self, package, old, role, path):
        if old in self.generic:
            if role == "top":
                fail(f"{path}: {old} is declared at package level")
            self.mapping[("", "member", old)] = self.generic[old]
            return self.generic[old]
        new = top_name(package, old) if role == "top" else member_name(old)
        self.mapping[(package if role == "top" else "", role, old)] = new
        if top_name(package, old) != member_name(old):
            key = (str(path), old, role)
            self.roles[key] = self.roles.get(key, 0) + 1
        return new

    def pinned_roles(self):
        """Role counts of the tokens that occur both as a declaration and a member."""
        top = {old for (_, old, role) in self.roles if role == "top"}
        member = {old for (_, old, role) in self.roles if role == "member"}
        return {key: count for key, count in self.roles.items() if key[1] in top & member}

    def rename_code(self, text, path, package, is_test):
        toks = tokens(text)
        imports = import_names(text)
        significant = [i for i, (kind, _) in enumerate(toks) if kind not in ("space", "comment")]
        order = {index: n for n, index in enumerate(significant)}
        braces = []
        case_depth = []
        line_first = set()
        at_line_start = True
        for i, (kind, value) in enumerate(toks):
            if kind == "space":
                if "\n" in value:
                    at_line_start = True
                continue
            if kind == "comment":
                if "\n" in value:
                    at_line_start = True
                continue
            if at_line_start:
                line_first.add(i)
            at_line_start = False

        def sig(n):
            return toks[significant[n]][1] if 0 <= n < len(significant) else ""

        out = []
        for i, (kind, value) in enumerate(toks):
            n = order.get(i)
            if kind == "other" and value == "{":
                before = sig(n - 1)
                if before in ("struct", "interface"):
                    braces.append(before)
                elif self.map_literal(toks, significant, n):
                    braces.append("map")
                else:
                    braces.append("block")
            elif kind == "other" and value == "}":
                if braces:
                    braces.pop()
            elif kind == "ident" and value == "case":
                case_depth.append(len(braces))
            elif kind == "other" and value == ":" and case_depth and case_depth[-1] == len(braces):
                case_depth.pop()
            if kind == "ident" and CODED.match(value) and not TEST_FUNC.match(value):
                out.append(self.rename_ident(toks, significant, n, i, value, path, package, imports, braces,
                                             line_first, bool(case_depth and case_depth[-1] == len(braces))))
            elif kind == "comment":
                out.append(self.rename_comment(value, path, package))
            elif kind == "string":
                out.append(self.rename_string(value, path, package, imports, is_test))
            else:
                out.append(value)
        return drop_redundant_aliases("".join(out))

    def rename_ident(self, toks, significant, n, i, value, path, package, imports, braces, line_first, in_case):
        def sig(k):
            return toks[significant[k]][1] if 0 <= k < len(significant) else ""

        before, after = sig(n - 1), sig(n + 1)
        if before == ".":
            qualifier = sig(n - 2)
            if qualifier in imports:
                return self.name(imports[qualifier], value, "top", path)
            return self.name(package, value, "member", path)
        if before == ")" and after == "(" and self.method_name(toks, significant, n):
            return self.name(package, value, "member", path)
        context = braces[-1] if braces else ""
        if context == "struct" and i in line_first:
            return self.name(package, value, "member", path)
        if context == "interface" and i in line_first and after == "(":
            return self.name(package, value, "member", path)
        if context == "block" and after == ":" and before in ("{", ",") and not in_case:
            return self.name(package, value, "member", path)
        if value in self.tops[package]:
            return self.name(package, value, "top", path)
        return self.name(package, value, "member", path)

    @staticmethod
    def map_literal(toks, significant, n):
        """A brace that opens a map composite literal: map[K]V{."""
        k = n - 1
        while k >= 0:
            value = toks[significant[k]][1]
            if value == "map":
                return True
            if value in ("(", ")", ",", "=", ":", "{", "}", ";", "return"):
                return False
            k -= 1
        return False

    @staticmethod
    def method_name(toks, significant, n):
        """A name right after the receiver of a method declaration."""
        depth = 0
        k = n - 1
        while k >= 0:
            value = toks[significant[k]][1]
            if value == ")":
                depth += 1
            elif value == "(":
                depth -= 1
                if depth == 0:
                    return k > 0 and toks[significant[k - 1]][1] == "func"
            k -= 1
        return False

    def word_in_text(self, word, package, imports, before):
        if word in self.generic:
            return self.generic[word]
        if before.endswith(".") and not before.endswith(".."):
            qualifier = re.search(r"([A-Za-z_][A-Za-z0-9_]*)\.$", before)
            if qualifier and qualifier.group(1) in imports:
                return top_name(imports[qualifier.group(1)], word)
            if qualifier and qualifier.group(1) in IMPORT_NAMES.values():
                return top_name(qualifier.group(1), word)
            return member_name(word)
        if word in self.tops.get(package, ()):
            return top_name(package, word)
        for other in ("localworker", "transport"):
            if word in self.tops[other]:
                return top_name(other, word)
        return member_name(word)

    def rename_comment(self, value, path, package):
        def word(match):
            article, old = match.group(1), match.group(2)
            if TEST_FUNC.match(old):
                return match.group()
            new = self.word_in_text(old, package, {}, value[:match.start(2)])
            if article:
                vowel = new[:1].lower() in "aeiou"
                article = ("An " if vowel else "A ") if article[0] == "A" else ("an " if vowel else "a ")
            return (article or "") + new

        value = ARTICLE_WORD.sub(word, value)
        return COMMAND_WORD.sub("passive-capture-smoke", area_prose(value))

    def rename_string(self, value, path, package, imports, is_test):
        if value == CAPTURE_IMPORT_OLD:
            return CAPTURE_IMPORT_NEW
        if not is_test:
            return value
        for old, new, _ in FILE_MOVES:
            if value == f'"{old.name}"':
                return f'"{new.name}"'
        value = MESSAGE_TAG.sub(r"\1", value)
        value = WORD.sub(lambda m: self.word_in_text(m.group(), package, imports, value[:m.start()]), value)
        return area_prose(PREVIOUS_SLICE_WORD.sub("", value))


def area_prose(value):
    """Name the area instead of a bare work-item code in prose."""
    for old, new in ARTICLES:
        value = value.replace(old, new)
    for code, words in AREA_WORDS.items():
        value = re.sub(rf"\b{code}\b", words, value)
    for old, new in DOUBLED:
        value = value.replace(old, new)
    return value


def drop_redundant_aliases(text):
    def drop(match):
        alias, path = match.group(2), match.group(3)
        if alias == path.rsplit("/", 1)[-1]:
            return f'{match.group(1)}"{path}"'
        return match.group(0)

    text = re.sub(r'^(\t)([A-Za-z_][A-Za-z0-9_]*) "([^"]+)"$', drop, text, flags=re.M)
    return re.sub(r'^(import )([A-Za-z_][A-Za-z0-9_]*) "([^"]+)"$', drop, text, flags=re.M)


def substitute(text, old, new, path, count=1):
    if text.count(old) != count:
        fail(f"{path}: expected {count} of {old!r}, found {text.count(old)}")
    return text.replace(old, new)


def check_mapping(renamer, tops_untouched):
    """Renamed names are valid, distinct per package and collision free."""
    seen, problems = {}, []
    for (package, role, old), new in sorted(renamer.mapping.items()):
        if new in KEYWORDS or new in PREDECLARED:
            problems.append(f"{old} -> {new} is reserved")
        if TERMINOLOGY.match(new):
            problems.append(f"{old} -> {new} is a legacy terminology word")
        if not re.match(r"^[A-Za-z_][A-Za-z0-9_]*$", new) or CODE.search(new) and not TEST_FUNC.match(new):
            problems.append(f"{old} -> {new} is not a clean identifier")
        if role == "top":
            if new in tops_untouched[package]:
                problems.append(f"{package}: {old} -> {new} collides with an existing package-level name")
            other = seen.get((package, new))
            if other and other != old:
                problems.append(f"{package}: {other} and {old} both become {new}")
            seen[(package, new)] = old
    if problems:
        fail("\n".join(sorted(set(problems))))


def verify(root):
    remaining = []
    for _, new, _ in FILE_MOVES:
        text = (root / new).read_text()
        for receiver in re.findall(r"^func \((self|this|me) ", text, re.M):
            remaining.append(f"{new}: generic receiver {receiver}")
        for kind, value in tokens(text):
            if kind == "ident" and CODED.match(value) and not TEST_FUNC.match(value):
                remaining.append(f"{new}: identifier {value}")
            if kind == "comment" and re.search(r"(?i)\baa2[3-9][A-Za-z]|\bAA2[3-9]\b", value):
                remaining.append(f"{new}: comment {value}")
    if remaining:
        fail("coded names remain:\n" + "\n".join(remaining))


def gofmt_binary():
    found = shutil.which("gofmt")
    if found:
        return found
    goroot = subprocess.run(["go", "env", "GOROOT"], check=True, capture_output=True, text=True).stdout.strip()
    candidate = Path(goroot) / "bin" / "gofmt"
    if not candidate.exists():
        fail("gofmt not found")
    return str(candidate)


def changelog_entry(renamer):
    moves = ", ".join(f"`{old.name}` → `{new.name}`" for old, new, _ in FILE_MOVES[:10])
    qualified_names = sorted({(old, new) for (package, role, old), new in renamer.mapping.items()
                              if role == "top" and package == "localworker" and new != member_name(old)
                              and old[:1].isupper()})
    exported_qualified = ", ".join(f"`{old}` → `{new}`" for old, new in qualified_names)
    text = (
        "Rename the work-item-coded local worker, local GPIO transport and smoke command files and "
        "identifiers to behavior names. The command directory `cmd/aa29-smoke` becomes "
        "`cmd/passive-capture-smoke` (its internal `capture` package moves with it; the file names "
        "inside are unchanged) and the ignored built binary is `/passive-capture-smoke`. Files in "
        f"`localworker` and `transport`: {moves}. Identifiers drop the `AA23`, `AA24`, `AA25`, `AA26` "
        "and `AA29` codes (for example `AA23Registry` → `Registry`, `ErrAA24Full` → `ErrFull`, "
        "`AA25NewEngine` → `NewEngine`, `AA26ErrWireBusy` → `ErrWireBusy`, `aa24Lane` → `lane`). "
        "Package-level names of the local GPIO backend take `LocalGPIO` instead of the code "
        "(`AA29Backend` → `LocalGPIOBackend`, `AA29PlatformProfile` → `LocalGPIOPlatformProfile`, "
        "`AA29GPIODescriptor` → `LocalGPIODescriptor`, `AA29ErrClosed` → `ErrLocalGPIOClosed`, "
        "`AA29Open` → `OpenLocalGPIO`), while their fields and methods drop it (`AA29Stats()` → "
        "`Stats()`, `AA29Lease.AA29ProfileDigest` → `LocalGPIOLease.ProfileDigest`). Exported "
        "package-level names that would collide inside `localworker` take the area of their file "
        f"(Plan, IPC, Engine, Responder): {exported_qualified}; the fields and methods that share "
        "these tokens drop the code, and unexported collisions follow the same rule. Import aliases "
        "that only carried the code are removed, comments name the area instead of the code, and "
        "test messages lose their leading code tag; test function names are unchanged. The "
        "`aa29_local_gpio` build tag, the `aa29.profile.1` digest domain, the flags, the report JSON "
        "keys and every usage and error text are unchanged. `scripts/rename_local_worker_names.py` "
        "reproduces the change and prints the full identifier map with `--print-map`."
    )
    wrapped = textwrap.fill(text, width=78, initial_indent="- ", subsequent_indent="  ",
                            break_on_hyphens=False, break_long_words=False)
    return wrapped + "\n\n"


def rename(root, print_map):
    for old, new, _ in FILE_MOVES:
        if not (root / old).is_file():
            fail(f"missing {old}; the tree is not in the pre-rename state")
        if (root / new).exists():
            fail(f"{new} already exists")
    if (root / NEW_COMMAND).exists():
        fail(f"{NEW_COMMAND} already exists")
    changelog = root / "CHANGELOG.md"
    log = changelog.read_text()
    if log.count(CHANGELOG_MARKER) != 1:
        fail("CHANGELOG.md has no single Unreleased section")
    ignore = (root / GITIGNORE).read_text()
    if ignore.count(GITIGNORE_OLD) != 1:
        fail(".gitignore has no single /aa29-smoke rule")

    moved = {old for old, _, _ in FILE_MOVES}
    sources, tops, tops_untouched = {}, {}, {}
    for package, directory in PACKAGE_DIRS.items():
        tops[package], tops_untouched[package] = set(), set()
        for path in sorted((root / directory).glob("*.go")):
            relative = path.relative_to(root)
            text = path.read_text()
            names = top_level_names(text)
            tops[package] |= {name for name in names if CODED.match(name)}
            if relative not in moved:
                tops_untouched[package] |= names
            else:
                tops_untouched[package] |= {name for name in names if not CODED.match(name)}
    for old, _, _ in FILE_MOVES:
        text = (root / old).read_text()
        for before, after in COMMENT_SUBS.get(old, []):
            text = substitute(text, before, after, old)
        if old in NOLINT_FILES:
            text = substitute(text, NOLINT, "", old, NOLINT_FILES[old])
        sources[old] = text

    renamer = Renamer(tops, generic_names([(sources[old], package) for old, _, package in FILE_MOVES]))
    results = {}
    for old, new, package in FILE_MOVES:
        results[new] = renamer.rename_code(sources[old], old, package, old.name.endswith("_test.go"))
    for old, new, _ in FILE_MOVES:
        before = [v for k, v in tokens(sources[old]) if k == "string" and not old.name.endswith("_test.go")]
        after = [v for k, v in tokens(results[new]) if k == "string" and not old.name.endswith("_test.go")]
        changed = [(b, a) for b, a in zip(before, after) if b != a]
        if len(before) != len(after) or any(b != CAPTURE_IMPORT_OLD for b, _ in changed):
            fail(f"{old}: a production string literal changed: {changed}")
    check_mapping(renamer, tops_untouched)
    if renamer.pinned_roles() != ROLE_COUNTS:
        fail("ambiguous token roles differ from the pinned counts:\n" +
             "\n".join(f"    ({k[0]!r}, {k[1]!r}, {k[2]!r}): {v}," for k, v in sorted(renamer.pinned_roles().items())))

    doc = "".join(renamer.rename_comment(value, DOC, "") if kind == "comment" else value
                  for kind, value in tokens((root / DOC).read_text()))

    (root / OLD_COMMAND).rename(root / NEW_COMMAND)
    for old, new, _ in FILE_MOVES:
        current = root / NEW_COMMAND / old.relative_to(OLD_COMMAND) if old.is_relative_to(OLD_COMMAND) else root / old
        current.rename(root / new)
        (root / new).write_text(results[new])
    (root / DOC).write_text(doc)
    subprocess.run([gofmt_binary(), "-w"] + [str(root / new) for _, new, _ in FILE_MOVES] + [str(root / DOC)],
                   check=True)
    (root / GITIGNORE).write_text(ignore.replace(GITIGNORE_OLD, GITIGNORE_NEW, 1))
    changelog.write_text(log.replace(CHANGELOG_MARKER, CHANGELOG_MARKER + changelog_entry(renamer), 1))
    verify(root)

    if print_map:
        for (package, role, old), new in sorted(renamer.mapping.items(), key=lambda item: (item[0][2], item[0][1])):
            if new != old:
                where = f" ({role}{', ' + package if package else ''})"
                print(f"{old} -> {new}{where}")
    exported = {old for (_, _, old) in renamer.mapping if old[:1].isupper()}
    unexported = {old for (_, _, old) in renamer.mapping if old[:1].islower()}
    print(f"renamed {len(exported)} exported and {len(unexported)} unexported identifiers in {len(FILE_MOVES)} files",
          file=sys.stderr)


# Occurrences of tokens that name both a package-level declaration and a
# member, by file and role.
ROLE_COUNTS = {
    ('cmd/aa29-smoke/internal/capture/capture.go', 'AA25Stats', 'top'): 1,
    ('cmd/aa29-smoke/internal/capture/capture_test.go', 'AA25Stats', 'top'): 1,
    ('cmd/aa29-smoke/smoke_linux_arm64.go', 'AA29Ordering', 'top'): 2,
    ('cmd/aa29-smoke/smoke_linux_arm64_test.go', 'AA29Ordering', 'top'): 2,
    ('cmd/aa29-smoke/smoke_linux_arm64_test.go', 'AA29ProfileDigest', 'top'): 4,
    ('localworker/aa24_ipc.go', 'AA24Limits', 'member'): 11,
    ('localworker/aa24_ipc.go', 'AA24Limits', 'top'): 6,
    ('localworker/aa24_ipc_test.go', 'AA24Limits', 'member'): 6,
    ('localworker/aa24_ipc_test.go', 'AA24Limits', 'top'): 22,
    ('localworker/aa25_engine.go', 'AA25Stats', 'member'): 1,
    ('localworker/aa25_engine.go', 'AA25Stats', 'top'): 3,
    ('localworker/aa25_engine.go', 'aa25FrameCRC', 'member'): 9,
    ('localworker/aa25_engine.go', 'aa25FrameCRC', 'top'): 7,
    ('localworker/aa25_engine.go', 'aa25FrameNN', 'member'): 3,
    ('localworker/aa25_engine.go', 'aa25FrameNN', 'top'): 3,
    ('localworker/aa25_engine.go', 'aa25FramePB', 'member'): 3,
    ('localworker/aa25_engine.go', 'aa25FramePB', 'top'): 3,
    ('localworker/aa25_engine.go', 'aa25FrameSB', 'member'): 3,
    ('localworker/aa25_engine.go', 'aa25FrameSB', 'top'): 3,
    ('localworker/aa25_engine.go', 'aa25FrameSource', 'member'): 3,
    ('localworker/aa25_engine.go', 'aa25FrameSource', 'top'): 4,
    ('localworker/aa25_engine_test.go', 'AA25Stats', 'member'): 17,
    ('localworker/aa25_engine_test.go', 'aa25FramePB', 'member'): 1,
    ('localworker/aa25_engine_test.go', 'aa25FrameSB', 'member'): 1,
    ('localworker/aa26_responder.go', 'AA26Stats', 'member'): 1,
    ('localworker/aa26_responder.go', 'AA26Stats', 'top'): 3,
    ('localworker/aa26_responder_test.go', 'AA25Stats', 'member'): 1,
    ('localworker/aa26_responder_test.go', 'AA26Stats', 'member'): 10,
    ('transport/aa29_local_gpio_linux_arm64.go', 'AA29Ordering', 'member'): 3,
    ('transport/aa29_local_gpio_linux_arm64.go', 'AA29Ordering', 'top'): 4,
    ('transport/aa29_local_gpio_linux_arm64.go', 'AA29ProfileDigest', 'member'): 2,
    ('transport/aa29_local_gpio_linux_arm64.go', 'AA29ProfileDigest', 'top'): 2,
    ('transport/aa29_local_gpio_linux_arm64.go', 'AA29Stats', 'member'): 1,
    ('transport/aa29_local_gpio_linux_arm64.go', 'AA29Stats', 'top'): 3,
    ('transport/aa29_local_gpio_linux_arm64.go', 'aa29Admit', 'top'): 2,
    ('transport/aa29_local_gpio_linux_arm64_test.go', 'AA29Ordering', 'member'): 5,
    ('transport/aa29_local_gpio_linux_arm64_test.go', 'AA29Ordering', 'top'): 2,
    ('transport/aa29_local_gpio_linux_arm64_test.go', 'AA29ProfileDigest', 'member'): 4,
    ('transport/aa29_local_gpio_linux_arm64_test.go', 'AA29ProfileDigest', 'top'): 5,
    ('transport/aa29_local_gpio_linux_arm64_test.go', 'AA29Stats', 'member'): 4,
    ('transport/aa29_local_gpio_linux_arm64_test.go', 'aa29Admit', 'member'): 5,
}


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("root", nargs="?", type=Path, default=Path(__file__).resolve().parent.parent)
    parser.add_argument("--print-map", action="store_true", help="print the identifier map after renaming")
    arguments = parser.parse_args()
    rename(arguments.root.resolve(), arguments.print_map)
