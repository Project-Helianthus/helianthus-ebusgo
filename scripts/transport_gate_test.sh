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

check_gate() {
  local name="$1" files="$2" expected_status="$3" expected_output="$4"
  local output status=0
  output="$(
    TEST_CHANGED_FILES="${files}" TRANSPORT_GATE_BASE_REF=synthetic-base \
      TRANSPORT_GATE_OWNER_OVERRIDE='' TRANSPORT_GATE_OWNER_REASON='' \
      TRANSPORT_MATRIX_REPORT='' bash "${repo_root}/scripts/transport_gate.sh" 2>&1
  )" || status=$?
  if [[ "${status}" -ne "${expected_status}" || "${output}" != "${expected_output}" ]]; then
    printf 'FAIL: %s (exit %s): %s\n' "${name}" "${status}" "${output}" >&2
    exit 1
  fi
  printf 'PASS: %s\n' "${name}"
}

not_triggered='transport gate: not triggered.'
report_required='transport gate: TRANSPORT_MATRIX_REPORT is required for transport/protocol changes.'
check_gate 'test-only diff' $'transport/example_test.go\nprotocol/example_test.go' 0 "${not_triggered}"
check_gate 'production transport file' 'transport/example.go' 1 "${report_required}"
check_gate 'mixed diff' $'transport/example_test.go\ntransport/example.go' 1 "${report_required}"
check_gate 'production before test' $'transport/example.go\ntransport/example_test.go' 1 "${report_required}"
check_gate 'production protocol file' 'protocol/example.go' 1 "${report_required}"
check_gate 'transport production-to-test rename' $'transport/example.go\ntransport/example_test.go' 1 "${report_required}"
check_gate 'protocol production-to-test rename' $'protocol/example.go\nprotocol/example_test.go' 1 "${report_required}"
check_gate 'test-to-test rename' $'transport/old_test.go\ntransport/new_test.go' 0 "${not_triggered}"
check_gate 'unqualified fixture remains gated' 'transport/testdata/example.json' 1 "${report_required}"
check_gate 'non-test suffix remains gated' 'transport/example_test.go.txt' 1 "${report_required}"
check_gate 'unrelated file' 'scripts/example.sh' 0 "${not_triggered}"
