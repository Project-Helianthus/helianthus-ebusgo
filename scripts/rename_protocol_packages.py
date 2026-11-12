#!/usr/bin/env python3
"""Reproduce the protocol package rename from a pre-rename repository tree."""

import argparse
from pathlib import Path


def rename(root):
    for old, new in (("telegram_fsm", "telegramfsm"),
                     ("ebus_standard", "ebusstandard")):
        source = root / "protocol" / old
        target = root / "protocol" / new
        if source.exists():
            if target.exists():
                raise SystemExit(f"Both package directories exist: {old}, {new}")
            source.rename(target)
    for path in sorted(root.rglob("*.go")):
        if any(part.startswith(".") for part in path.relative_to(root).parts):
            continue
        before = path.read_text()
        after = before.replace("protocol/telegram_fsm", "protocol/telegramfsm")
        after = after.replace("protocol/ebus_standard", "protocol/ebusstandard")
        after = after.replace("package telegram_fsm", "package telegramfsm")
        after = after.replace("Package telegram_fsm", "Package telegramfsm")
        if path == root / "protocol/ebusstandard/types/types.go":
            after = after.replace("ebus_standard namespace", "ebusstandard namespace")
            after = after.replace("ebus_standard primitive", "ebusstandard primitive")
        if after != before:
            path.write_text(after)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("root", nargs="?", type=Path,
                        default=Path(__file__).resolve().parent.parent)
    rename(parser.parse_args().root.resolve())
