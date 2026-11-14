#!/usr/bin/env python3
"""Reproduce the transport capability evidence rename from a pre-rename tree.

The opt-in transport capability descriptor carried the work-item code AA22 in
its file names, in every exported identifier, in its struct fields and method
names, in its import aliases and in its locals. A few package comments still
cited external planning documents. This script renames the descriptor to
behavior names, drops those references and changes nothing else:

* transport/aa22_capabilities.go and its test move to
  transport/capability_evidence.go and transport/capability_evidence_test.go;
* exported identifiers follow one table (EXPORTED): the evidence type and its
  values become CapabilityEvidence and Evidence*, the transport kind becomes
  BackendKind with Kind* values, the encoding becomes StreamEncoding with
  Encoding* values, and the descriptor, its fields, the provider interface,
  its method, the query function and the deadline predicate drop the code;
* locals, receivers, test fixtures and test function names drop the code, with
  the per-file exceptions in LOCALS where the plain name would be a keyword or
  would name two different things in one file;
* the import aliases that only carried the code are dropped and the leading
  work-item tag of test messages goes;
* the local GPIO backend, the passive-capture smoke command and their tests
  follow the new names;
* comments that cited external planning documents describe the decision
  instead or are removed; no code changes in those files;
* gofmt is applied and one CHANGELOG entry is added.

Behavior, wire output, build tags and every other exported name are unchanged.
The work-item code guard test that the rename satisfies is not in the
pre-rename tree; the script adds it (GUARD_PREFIX) to the renamed test file.

It runs offline, needs only Python 3 and gofmt, and refuses to run unless the
tree is in the pre-rename state. Usage: rename_capability_names.py [ROOT]
[--print-map]
"""

import argparse
import re
import shutil
import subprocess
import sys
from pathlib import Path

OLD_SOURCE = Path("transport/aa22_capabilities.go")
OLD_TEST = Path("transport/aa22_capabilities_test.go")
NEW_SOURCE = Path("transport/capability_evidence.go")
NEW_TEST = Path("transport/capability_evidence_test.go")
FILE_MOVES = [(OLD_SOURCE, NEW_SOURCE), (OLD_TEST, NEW_TEST)]

GPIO = Path("transport/local_gpio_linux_arm64.go")
GPIO_TEST = Path("transport/local_gpio_linux_arm64_test.go")
CAPTURE = Path("cmd/passive-capture-smoke/internal/capture/capture.go")
CAPTURE_TEST = Path("cmd/passive-capture-smoke/internal/capture/capture_test.go")
SMOKE_TEST = Path("cmd/passive-capture-smoke/smoke_linux_arm64_test.go")
CALLERS = [GPIO, GPIO_TEST, CAPTURE, CAPTURE_TEST, SMOKE_TEST]

CODE = re.compile(r"(AA|aa)22")
TOKEN = re.compile(r"\b[A-Za-z_][A-Za-z0-9_]*\b")

# Exported identifiers, struct fields and methods: one name in every file.
EXPORTED = {
    "AA22Evidence": "CapabilityEvidence",
    "AA22Unknown": "EvidenceUnknown",
    "AA22Hypothesis": "EvidenceHypothesis",
    "AA22Proven": "EvidenceProven",
    "AA22TransportKind": "BackendKind",
    "AA22KindUnknown": "KindUnknown",
    "AA22KindLocalRaw": "KindLocalRaw",
    "AA22KindENH": "KindENH",
    "AA22KindENS": "KindENS",
    "AA22KindTCP": "KindTCP",
    "AA22KindUDP": "KindUDP",
    "AA22KindEBUSDCommand": "KindEBUSDCommand",
    "AA22Encoding": "StreamEncoding",
    "AA22EncodingUnknown": "EncodingUnknown",
    "AA22EncodingRaw": "EncodingRaw",
    "AA22EncodingLogical": "EncodingLogical",
    "AA22Capabilities": "Capabilities",
    "AA22Kind": "Kind",
    "AA22PhysicalArbitration": "PhysicalArbitration",
    "AA22AutonomousPreparedResponse": "AutonomousPreparedResponse",
    "AA22ObservedEcho": "ObservedEcho",
    "AA22CaptureTimestamps": "CaptureTimestamps",
    "AA22QualifiedTiming": "QualifiedTiming",
    "AA22CapabilityProvider": "CapabilityProvider",
    "AA22TransportCapabilities": "TransportCapabilities",
    "AA22QueryCapabilities": "QueryCapabilities",
    "AA22DeadlineSuitable": "DeadlineSuitable",
}

# AA22Encoding names both the type and the struct field. The field is the one
# that follows a selector dot, is a composite-literal key or starts a struct
# field line; every other position, comments included, is the type. The counts
# are pinned per file.
FIELD_ONLY = {"AA22Encoding": "Encoding"}
FIELD_POSITIONS = {OLD_SOURCE: 5, OLD_TEST: 3, GPIO: 1, GPIO_TEST: 1}
TYPE_POSITIONS = {OLD_SOURCE: 4, OLD_TEST: 2}

# Unexported names, per file: the code is dropped unless listed otherwise.
LOCALS = {
    OLD_SOURCE: {
        "aa22reflect": "reflect",
        "aa22Transport": "backend",
        "aa22Provider": "provider",
        "aa22OK": "ok",
        "aa22Value": "value",
        "aa22Result": "result",
        "aa22Claim": "claim",
        "aa22Capabilities": "capabilities",
    },
    OLD_TEST: {
        "aa22testing": "testing",
        "aa22Provider": "fakeProvider",
        "aa22ProviderValue": "provider",
        "aa22Capabilities": "capabilities",
        "aa22ResponderOnly": "responderOnly",
        "aa22Payload": "payload",
        "aa22PositiveCapabilities": "positiveCapabilities",
        "aa22T": "t",
        "aa22NilProvider": "nilProvider",
        "aa22Transport": "backend",
        "aa22Got": "got",
        "aa22Field": "field",
        "aa22Evidence": "evidence",
        "aa22Input": "input",
        "aa22Expected": "expected",
        "aa22InputFields": "inputFields",
        "aa22ExpectedFields": "expectedFields",
        "aa22WantDeadline": "wantDeadline",
        "aa22Combination": "combination",
        "aa22Fields": "fields",
        "aa22Digits": "digits",
        "aa22Want": "want",
        "aa22Case": "tc",
        "aa22Kind": "kind",
        "aa22Deadline": "deadline",
        "aa22Encoding": "encoding",
        "TestAA22AbsentCapabilities": "TestAbsentCapabilities",
        "TestAA22IndependentClaims": "TestIndependentClaims",
        "TestAA22ClaimCombinations": "TestClaimCombinations",
        "TestAA22TransportKinds": "TestTransportKinds",
        "TestAA22Encodings": "TestEncodings",
    },
}

GUARD_PREFIX = """import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// workItemCode matches a two-letter work-item code followed by its number at
// the start of a name or of a word inside it, for example XY12Name, ErrXY12 or
// xy12_name.go, while plain counts such as Uint32 or arm64 stay accepted.
var workItemCode = regexp.MustCompile(`(^|[a-z0-9_])[A-Z]{2}[0-9]{2}|(^|_)[a-z]{2}[0-9]{2}`)

// TestNoWorkItemCodeInExportedNamesOrFiles lists every Go file name of the
// package and every exported name its production files declare (types,
// functions, methods, constants, variables, struct fields and interface
// methods, under every build constraint) and fails on a work-item code.
func TestNoWorkItemCodeInExportedNamesOrFiles(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	fileSet := token.NewFileSet()
	checked := 0
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || filepath.Ext(name) != ".go" {
			continue
		}
		if workItemCode.MatchString(name) {
			t.Errorf("file name %s carries a work-item code", name)
		}
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fileSet, name, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(file, func(node ast.Node) bool {
			var idents []*ast.Ident
			switch n := node.(type) {
			case *ast.FuncDecl:
				idents = append(idents, n.Name)
			case *ast.TypeSpec:
				idents = append(idents, n.Name)
			case *ast.ValueSpec:
				idents = append(idents, n.Names...)
			case *ast.Field:
				idents = append(idents, n.Names...)
			}
			for _, ident := range idents {
				if !ident.IsExported() {
					continue
				}
				checked++
				if workItemCode.MatchString(ident.Name) {
					t.Errorf("exported name %s in %s carries a work-item code", ident.Name, name)
				}
			}
			return true
		})
	}
	if checked == 0 {
		t.Fatal("no exported name was checked")
	}
	for _, accepted := range []string{"Uint32", "IsV31", "local_gpio_linux_arm64.go", "Sum256"} {
		if workItemCode.MatchString(accepted) {
			t.Fatalf("%s is not a work-item code", accepted)
		}
	}
	for _, rejected := range []string{"XY12Name", "ErrXY12", "xy12_name.go", "name_xy12.go"} {
		if !workItemCode.MatchString(rejected) {
			t.Fatalf("%s carries a work-item code", rejected)
		}
	}
}

"""

# Exact substitutions applied before the identifier rename.
SOURCE_SUBS = {
    OLD_SOURCE: [
        ('import aa22reflect "reflect"\n', 'import "reflect"\n'),
        ("only by an AA22CapabilityProvider;", "only by a AA22CapabilityProvider;"),
    ],
    OLD_TEST: [
        ('import aa22testing "testing"\n\n', GUARD_PREFIX),
        ('panic("aa22 query attempted emission")', 'panic("query attempted emission")'),
    ]
    + [
        (f'Fatal{suffix}("aa22 ', f'Fatal{suffix}("')
        for suffix in ("f", "")
    ],
    CAPTURE: [
        ("onto AA22\n// evidence.", "onto capability\n// evidence."),
    ],
}
SUB_COUNTS = {(OLD_TEST, 'Fatalf("aa22 '): 6, (OLD_TEST, 'Fatal("aa22 '): 3}

# Comments that cited external planning documents: (file, pattern,
# replacement). Patterns match the quoted document path without spelling it.
PLAN_PATH = r"`[a-z-]+/[a-z0-9.-]+/\n// [^`]+`"
COMMENT_SUBS = [
    (
        Path("transport/responder.go"),
        r"// responder lane per decision doc\n// " + PLAN_PATH + r" §6\.1 \(option_go_transport_scoped\)\.\n",
        "// responder lane, under the transport-scoped responder decision\n// (option_go_transport_scoped).\n",
    ),
    (
        Path("transport/responder_test.go"),
        r"// These tests describe the end-state contract for PR-A of M4c1 per decision\n// doc "
        + r"`[a-z-]+/[a-z0-9.-]+/\n// [^`]+`"
        + r" §6\.1\. They are expected to FAIL until\n",
        "// These tests describe the end-state contract for PR-A of M4c1 under the\n"
        "// transport-scoped responder decision. They are expected to FAIL until\n",
    ),
    (
        Path("protocol/responder/frame_decoder_test.go"),
        r"//\n// See " + PLAN_PATH + r" §6\.1\.\n",
        "",
    ),
    (
        Path("protocol/responder/doc.go"),
        r"// M4c1 PR-B: GREEN-phase implementation per decision doc\n// " + PLAN_PATH + r" §6\.1\.\n",
        "// M4c1 PR-B: GREEN-phase implementation under the transport-scoped\n// responder decision.\n",
    ),
    (
        Path("protocol/vaillant/b503/doc.go"),
        r"// Package b503 is the M1_DECODER deliverable for cruise-run\n"
        r"// Project-Helianthus/[a-z-]+#[0-9]+ \(plan\n"
        r"// [a-z0-9-]+, canonical SHA [0-9a-f]+\)\.\n"
        r"//\n"
        r"// Scope and design are locked by decisions AD01\.\.AD05 in\n"
        r"// `[^`]+`:\n",
        "// Package b503 decodes and encodes the Vaillant B503 error and\n"
        "// service-message namespace.\n"
        "//\n"
        "// Scope and design are locked by decisions AD01..AD05:\n",
    ),
    (
        Path("protocol/vaillant/b503/doc.go"),
        r"//\n// Canonical plan SHA-256:\n//\n//\t[0-9a-f]{64}\n",
        "",
    ),
]
RETIRED = re.compile(r"[a-z]+-plans[/#]|Canonical plan|canonical SHA")

CHANGELOG_MARKER = "## Unreleased\n\n"
CHANGELOG_ENTRY = """\
- Rename the work-item-coded transport capability descriptor to behavior
  names directly, with no alias and no migration step; behavior, wire output
  and build tags are unchanged:
  - files: `transport/aa22_capabilities.go` → `transport/capability_evidence.go`
    and `transport/aa22_capabilities_test.go` →
    `transport/capability_evidence_test.go`;
  - evidence: `AA22Evidence` → `CapabilityEvidence`, `AA22Unknown` →
    `EvidenceUnknown`, `AA22Hypothesis` → `EvidenceHypothesis`, `AA22Proven`
    → `EvidenceProven`;
  - backend kind: `AA22TransportKind` → `BackendKind`, `AA22KindUnknown` →
    `KindUnknown`, `AA22KindLocalRaw` → `KindLocalRaw`, `AA22KindENH` →
    `KindENH`, `AA22KindENS` → `KindENS`, `AA22KindTCP` → `KindTCP`,
    `AA22KindUDP` → `KindUDP`, `AA22KindEBUSDCommand` → `KindEBUSDCommand`;
  - stream encoding: `AA22Encoding` (type) → `StreamEncoding`,
    `AA22EncodingUnknown` → `EncodingUnknown`, `AA22EncodingRaw` →
    `EncodingRaw`, `AA22EncodingLogical` → `EncodingLogical`;
  - descriptor: `AA22Capabilities` → `Capabilities` with fields `AA22Kind` →
    `Kind`, `AA22Encoding` → `Encoding`, `AA22PhysicalArbitration` →
    `PhysicalArbitration`, `AA22AutonomousPreparedResponse` →
    `AutonomousPreparedResponse`, `AA22ObservedEcho` → `ObservedEcho`,
    `AA22CaptureTimestamps` → `CaptureTimestamps`, `AA22QualifiedTiming` →
    `QualifiedTiming`;
  - provider and queries: `AA22CapabilityProvider` → `CapabilityProvider`,
    method `AA22TransportCapabilities` → `TransportCapabilities` (also on
    `LocalGPIOBackend`), `AA22QueryCapabilities` → `QueryCapabilities`,
    method `AA22DeadlineSuitable` → `DeadlineSuitable`;
  - package comments no longer cite external planning documents.
  `scripts/rename_capability_names.py` reproduces the rename.

"""


def fail(message):
    print(f"rename_capability_names: {message}", file=sys.stderr)
    sys.exit(1)


def substitute(text, before, after, path):
    expected = SUB_COUNTS.get((path, before), 1)
    found = text.count(before)
    if found != expected:
        fail(f"{path}: expected {expected} of {before!r}, found {found}")
    return text.replace(before, after)


def field_position(text, start):
    """Report whether the token at start is a field or a composite-literal key."""
    if start > 0 and text[start - 1] == ".":
        return True
    line_end = text.find("\n", start)
    after = text[TOKEN.match(text, start).end():line_end]
    if re.match(r"\s*:[^=]", after):
        return True
    prefix = text[text.rfind("\n", 0, start) + 1:start]
    # A struct field line "Name Type".
    return not prefix.strip() and bool(re.match(r"\s+[A-Za-z*\[]", after))


def rename_text(text, path, counts):
    table = dict(EXPORTED)
    table.update(LOCALS.get(path, {}))

    def replace(match):
        name = match.group(0)
        if not CODE.search(name):
            return name
        if name in FIELD_ONLY:
            role = "field" if field_position(text, match.start()) else "type"
            counts[(path, role)] = counts.get((path, role), 0) + 1
            if role == "field":
                return FIELD_ONLY[name]
        if name not in table:
            fail(f"{path}: no rename rule for {name}")
        return table[name]

    return TOKEN.sub(replace, text)


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
    changelog = root / "CHANGELOG.md"
    log = changelog.read_text()
    if log.count(CHANGELOG_MARKER) != 1:
        fail("CHANGELOG.md has no single Unreleased section")

    results = {}
    counts = {}
    for path in [old for old, _ in FILE_MOVES] + CALLERS:
        text = (root / path).read_text()
        for before, after in SOURCE_SUBS.get(path, []):
            text = substitute(text, before, after, path)
        text = rename_text(text, path, counts)
        if CODE.search(text):
            fail(f"{path}: work-item code remains")
        results[path] = text
    expected = {(path, "field"): n for path, n in FIELD_POSITIONS.items()}
    expected.update({(path, "type"): n for path, n in TYPE_POSITIONS.items()})
    if counts != expected:
        fail(f"field and type positions {counts} differ from {expected}")

    comments = {}
    for path, pattern, replacement in COMMENT_SUBS:
        text = comments.get(path) or (root / path).read_text()
        text, found = re.subn(pattern, lambda _: replacement, text)
        if found != 1:
            fail(f"{path}: expected one retired plan reference matching {pattern!r}, found {found}")
        comments[path] = text
    for path, text in comments.items():
        if RETIRED.search(text):
            fail(f"{path}: a retired plan reference remains")

    destinations = dict(FILE_MOVES)
    for path, text in results.items():
        target = root / destinations.get(path, path)
        if path in destinations:
            (root / path).rename(target)
        target.write_text(text)
    for path, text in comments.items():
        (root / path).write_text(text)
    written = [str(root / destinations.get(path, path)) for path in results] + [str(root / p) for p in comments]
    subprocess.run([gofmt_binary(), "-w"] + written, check=True)
    changelog.write_text(log.replace(CHANGELOG_MARKER, CHANGELOG_MARKER + CHANGELOG_ENTRY, 1))

    if print_map:
        for old, new in sorted(EXPORTED.items()):
            print(f"{old} -> {new}")
        for old, new in FIELD_ONLY.items():
            print(f"{old} (field positions) -> {new}")
    print(f"renamed {len(EXPORTED) + len(FIELD_ONLY)} exported names in {len(results)} files and "
          f"dropped {len(COMMENT_SUBS)} retired plan references", file=sys.stderr)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("root", nargs="?", type=Path, default=Path(__file__).resolve().parent.parent)
    parser.add_argument("--print-map", action="store_true", help="print the identifier map after renaming")
    arguments = parser.parse_args()
    rename(arguments.root.resolve(), arguments.print_map)
