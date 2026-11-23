#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# Mock only Git's read-only inputs; run the complete gate without repository writes.
git() {
  case "$1" in
    rev-parse) return 0 ;;
    diff)
      # Fixtures list both sides of renames, as Git does with --no-renames.
      if [[ " $* " != *' --no-renames '* ]]; then
        echo 'FAIL: git diff must disable rename detection' >&2
        return 2
      fi
      printf '%s\n' "${TEST_CHANGED_FILES}"
      ;;
    *) return 2 ;;
  esac
}
export -f git

fixture_dir="$(mktemp -d)"
trap 'rm -rf "$fixture_dir"' EXIT

# Writes a synthetic 88-case matrix report. Modes alter one case, or the whole file.
write_report() {
  local path="$1" mode="$2"
  python3 - "$path" "$mode" <<'PY'
import json
import sys

path, mode = sys.argv[1], sys.argv[2]
if mode == "not-json":
    with open(path, "w", encoding="utf-8") as handle:
        handle.write("not json")
    raise SystemExit(0)
if mode == "no-cases":
    payload = {"summary": "synthetic"}
else:
    cases = []
    for number in range(1, 89):
        case = {"case_id": f"T{number:02d}", "outcome": "pass"}
        if mode == "planned" and number == 10:
            case["outcome"] = "planned"
        if mode == "required-fail" and number == 1:
            case["outcome"] = "xfail"
        if mode == "unexpected" and number == 10:
            case["outcome"] = "weird"
        cases.append(case)
    payload = {"cases": cases}
with open(path, "w", encoding="utf-8") as handle:
    json.dump(payload, handle)
PY
}

check_gate() {
  local name="$1" files="$2" expected_status="$3" expected_output="$4"
  local report="${5:-}" override="${6:-}" reason="${7:-}"
  local output status=0
  output="$(
    TEST_CHANGED_FILES="${files}" TRANSPORT_GATE_BASE_REF=synthetic-base \
      TRANSPORT_GATE_OWNER_OVERRIDE="${override}" TRANSPORT_GATE_OWNER_REASON="${reason}" \
      TRANSPORT_MATRIX_REPORT="${report}" bash "${repo_root}/scripts/transport_gate.sh" 2>&1
  )" || status=$?
  if [[ "${status}" -ne "${expected_status}" || "${output}" != "${expected_output}" ]]; then
    printf 'FAIL: %s (exit %s): %s\n' "${name}" "${status}" "${output}" >&2
    exit 1
  fi
  printf 'PASS: %s\n' "${name}"
}

not_triggered='transport gate: not triggered.'
not_run='transport gate: adapter matrix not run (no report), not a merge condition.'
check_gate 'test-only diff' $'transport/example_test.go\nprotocol/example_test.go' 0 "${not_triggered}"
check_gate 'production transport file' 'transport/example.go' 0 "${not_run}"
check_gate 'production transport test.go suffix' 'transport/latest.go' 0 "${not_run}"
check_gate 'mixed diff' $'transport/example_test.go\ntransport/example.go' 0 "${not_run}"
check_gate 'production before test' $'transport/example.go\ntransport/example_test.go' 0 "${not_run}"
check_gate 'production protocol file' 'protocol/example.go' 0 "${not_run}"
check_gate 'production protocol test.go suffix' 'protocol/contest.go' 0 "${not_run}"
check_gate 'transport production-to-test rename' $'transport/example.go\ntransport/example_test.go' 0 "${not_run}"
check_gate 'protocol production-to-test rename' $'protocol/example.go\nprotocol/example_test.go' 0 "${not_run}"
check_gate 'test-to-test rename' $'transport/old_test.go\ntransport/new_test.go' 0 "${not_triggered}"
check_gate 'unqualified fixture remains gated' 'transport/testdata/example.json' 0 "${not_run}"
check_gate 'non-test suffix remains gated' 'transport/example_test.go.txt' 0 "${not_run}"
check_gate 'unrelated file' 'scripts/example.sh' 0 "${not_triggered}"
check_gate 'vendor-neutral engine file is not triggered' 'transport/engine_cache.go' 0 "${not_triggered}"
check_gate 'vendor-neutral responder file is not triggered' 'transport/responder_timing.go' 0 "${not_triggered}"
check_gate 'vendor-neutral protocol responder file is not triggered' 'protocol/responder/frame.go' 0 "${not_triggered}"
check_gate 'engine file with gated transport file remains gated' $'transport/engine_cache.go\ntransport/latest.go' 0 "${not_run}"
check_gate 'adaptermux file is outside the matrix trigger' 'adaptermux/mux.go' 0 "${not_triggered}"
check_gate 'engine file is outside the matrix trigger' 'engine/scheduler.go' 0 "${not_triggered}"

missing_report="${fixture_dir}/absent-report.json"
check_gate 'missing report file is a recorded level' 'transport/example.go' 0 \
  "transport gate: report not found at ${missing_report}." "${missing_report}"

not_json_report="${fixture_dir}/not-json.json"
write_report "${not_json_report}" not-json
check_gate 'non-JSON report is a recorded level' 'transport/example.go' 0 \
  'transport gate: invalid matrix report (not JSON).' "${not_json_report}"

no_cases_report="${fixture_dir}/no-cases.json"
write_report "${no_cases_report}" no-cases
check_gate 'report without cases list is a recorded level' 'transport/example.go' 0 \
  'transport gate: invalid matrix report (missing cases list).' "${no_cases_report}"

pass_report="${fixture_dir}/pass.json"
write_report "${pass_report}" pass
check_gate 'complete matrix report passes' 'transport/example.go' 0 \
  "transport gate: PASS (required_active=6, ebusd_plain_nonblocking=2, proxy_single_informational=16, proxy_dual_informational=64, total=88, outcomes={'pass': 88})." \
  "${pass_report}"

planned_report="${fixture_dir}/planned.json"
write_report "${planned_report}" planned
check_gate 'planned matrix case is a recorded level' 'transport/example.go' 0 \
  'transport gate: matrix still has planned/not-run cases (1). sample=T10' "${planned_report}"

required_fail_report="${fixture_dir}/required-fail.json"
write_report "${required_fail_report}" required-fail
check_gate 'failed required active case is a recorded level' 'transport/example.go' 0 \
  'transport gate: required active adapter cases failed (1). sample=T01' "${required_fail_report}"

unexpected_report="${fixture_dir}/unexpected.json"
write_report "${unexpected_report}" unexpected
check_gate 'unexpected matrix outcome is a recorded level' 'transport/example.go' 0 \
  'transport gate: matrix has unexpected outcomes (1). sample=T10' "${unexpected_report}"

check_gate 'owner override without reason is a recorded level' 'transport/example.go' 0 \
  'transport gate override requires TRANSPORT_GATE_OWNER_REASON.' '' OVERRIDE_TRANSPORT_GATE_BY_OWNER ''
check_gate 'owner override with reason' 'transport/example.go' 0 \
  'transport gate: owner override active (synthetic reason).' '' OVERRIDE_TRANSPORT_GATE_BY_OWNER 'synthetic reason'

# Genuine validation errors are not matrix outcomes: a failing git read still stops the gate.
git_read_failure() {
  case "$1" in
    rev-parse) return 0 ;;
    *) return 2 ;;
  esac
}
git_failure_status=0
git_failure_output="$(
  git() { git_read_failure "$@"; }
  export -f git_read_failure git
  TEST_CHANGED_FILES='' TRANSPORT_GATE_BASE_REF=synthetic-base TRANSPORT_MATRIX_REPORT='' \
    bash "${repo_root}/scripts/transport_gate.sh" 2>&1
)" || git_failure_status=$?
if [[ "${git_failure_status}" -ne 2 ]]; then
  printf 'FAIL: unrelated git read failure (exit %s, want 2): %s\n' "${git_failure_status}" "${git_failure_output}" >&2
  exit 1
fi
printf 'PASS: unrelated git read failure still fails\n'
