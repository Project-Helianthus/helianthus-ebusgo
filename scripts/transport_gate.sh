#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${repo_root}"

base_ref="${TRANSPORT_GATE_BASE_REF:-origin/main}"
if ! git rev-parse --verify "${base_ref}" >/dev/null 2>&1; then
  base_ref="main"
fi
if ! git rev-parse --verify "${base_ref}" >/dev/null 2>&1; then
  echo "transport gate: base ref not found, skipping."
  exit 0
fi

changed_files="$(git diff --name-only "${base_ref}...HEAD")"
if [[ -z "${changed_files}" ]]; then
  echo "transport gate: no changes against ${base_ref}."
  exit 0
fi

requires_gate=0
while IFS= read -r file; do
  [[ -z "${file}" ]] && continue
  if [[ "${file}" =~ ^(transport/|protocol/) ]]; then
    requires_gate=1
    break
  fi
done <<< "${changed_files}"

if [[ "${requires_gate}" -eq 0 ]]; then
  echo "transport gate: not triggered."
  exit 0
fi

if [[ "${TRANSPORT_GATE_OWNER_OVERRIDE:-}" == "OVERRIDE_TRANSPORT_GATE_BY_OWNER" ]]; then
  if [[ -z "${TRANSPORT_GATE_OWNER_REASON:-}" ]]; then
    echo "transport gate override requires TRANSPORT_GATE_OWNER_REASON."
    exit 1
  fi
  echo "transport gate: owner override active (${TRANSPORT_GATE_OWNER_REASON})."
  exit 0
fi

report_path="${TRANSPORT_MATRIX_REPORT:-}"
if [[ -z "${report_path}" ]]; then
  echo "transport gate: TRANSPORT_MATRIX_REPORT is required for transport/protocol changes."
  exit 1
fi
if [[ ! -f "${report_path}" ]]; then
  echo "transport gate: report not found at ${report_path}."
  exit 1
fi

python3 - "${report_path}" <<'PY'
import json
import sys

path = sys.argv[1]
with open(path, "r", encoding="utf-8") as handle:
    payload = json.load(handle)

cases = payload.get("cases")
if not isinstance(cases, list):
    print("transport gate: invalid matrix report (missing cases list).")
    raise SystemExit(1)
if len(cases) != 88:
    print(f"transport gate: expected 88 cases, got {len(cases)}.")
    raise SystemExit(1)

def normalized_outcome(case):
    outcome = case.get("outcome")
    if isinstance(outcome, str) and outcome:
        return outcome
    status = case.get("status")
    if status == "passed":
        return "pass"
    if status == "planned":
        return "planned"
    return "fail"

case_by_id = {}
duplicate = []
for case in cases:
    case_id = case.get("case_id")
    if not isinstance(case_id, str) or not case_id:
        print("transport gate: invalid matrix report (case without case_id).")
        raise SystemExit(1)
    if case_id in case_by_id:
        duplicate.append(case_id)
    case_by_id[case_id] = case

if duplicate:
    preview = ",".join(sorted(set(duplicate))[:10])
    print(f"transport gate: duplicate case IDs ({len(set(duplicate))}). sample={preview}")
    raise SystemExit(1)

expected_ids = {f"T{number:02d}" for number in range(1, 89)}
actual_ids = set(case_by_id)
missing_ids = sorted(expected_ids - actual_ids)
unknown_ids = sorted(actual_ids - expected_ids)
if missing_ids or unknown_ids:
    if missing_ids:
        print(f"transport gate: missing matrix case IDs: {','.join(missing_ids[:10])}")
    if unknown_ids:
        print(f"transport gate: unknown matrix case IDs: {','.join(unknown_ids[:10])}")
    raise SystemExit(1)

required_active = {f"T{number:02d}" for number in range(1, 7)}
ebusd_plain_adapter = {"T07", "T08"}
proxy_single_info = {f"T{number:02d}" for number in range(9, 25)}
proxy_dual_info = {f"T{number:02d}" for number in range(25, 89)}
allowed_non_terminal = {"pass", "xfail", "xpass", "blocked-infra"}

required_failures = []
unexpected = []
planned = []
for case_id, case in sorted(case_by_id.items()):
    value = normalized_outcome(case)
    if value == "planned":
        planned.append(case_id)
        continue
    if value not in allowed_non_terminal:
        unexpected.append(case_id)
        continue
    if case_id in required_active and value not in {"pass", "xpass"}:
        required_failures.append(case_id)
    if case_id in ebusd_plain_adapter:
        infra_reason = case.get("infra_reason")
        if value == "blocked-infra" and infra_reason != "adapter_no_signal":
            unexpected.append(case_id)
        elif value not in {"pass", "xfail", "xpass", "blocked-infra"}:
            unexpected.append(case_id)

if planned:
    preview = ",".join(planned[:10])
    print(f"transport gate: matrix still has planned/not-run cases ({len(planned)}). sample={preview}")
    raise SystemExit(1)
if required_failures:
    preview = ",".join(required_failures[:10])
    print(f"transport gate: required active adapter cases failed ({len(required_failures)}). sample={preview}")
    raise SystemExit(1)
if unexpected:
    preview = ",".join(unexpected[:10])
    print(f"transport gate: matrix has unexpected outcomes ({len(unexpected)}). sample={preview}")
    raise SystemExit(1)

counts = {}
for case in cases:
    value = normalized_outcome(case)
    counts[value] = counts.get(value, 0) + 1

msg = (
    "transport gate: PASS "
    f"(required_active={len(required_active)}, "
    f"ebusd_plain_nonblocking={len(ebusd_plain_adapter)}, "
    f"proxy_single_informational={len(proxy_single_info)}, "
    f"proxy_dual_informational={len(proxy_dual_info)}, "
    f"total={len(cases)}, outcomes={counts})."
)
if counts.get("xpass", 0):
    msg += " review expected-failure list (xpass present)."
print(msg)
PY
