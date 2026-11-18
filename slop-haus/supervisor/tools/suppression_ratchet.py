"""Ratchet on pyright suppressions in src/ and tests/.

Fails when a suppression names no rule (pyright's `enableTypeIgnoreComments`
is off, so `# type: ignore` would not suppress anything), when the count rises
above tools/suppression_baseline.txt, or when it fell below it and the baseline
was not lowered. `--update` rewrites the baseline.
"""

import re
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
BASELINE = Path(__file__).resolve().parent / "suppression_baseline.txt"
SUPPRESSION = re.compile(r"#\s*(?:type|pyright):\s*ignore\b(\[[^\]]*\])?")
RULED = re.compile(r"#\s*pyright:\s*ignore\[[A-Za-z]+(?:\s*,\s*[A-Za-z]+)*\]")


def find_suppressions(root: Path = ROOT) -> list[tuple[str, int, str]]:
    found: list[tuple[str, int, str]] = []
    for directory in ("src", "tests"):
        for path in sorted((root / directory).rglob("*.py")):
            for number, line in enumerate(path.read_text().splitlines(), 1):
                match = SUPPRESSION.search(line)
                if match:
                    found.append((str(path.relative_to(root)), number, match.group(0)))
    return found


def main(argv: list[str], root: Path = ROOT, baseline_path: Path = BASELINE) -> int:
    found = find_suppressions(root)
    unruled = [(f, n) for f, n, text in found if not RULED.fullmatch(text)]
    for path, number in unruled:
        print(f"{path}:{number}: use `# pyright: ignore[rule]`, not a bare or mypy-style ignore")
    if unruled:
        return 1
    count = len(found)
    if "--update" in argv:
        baseline_path.write_text(f"{count}\n")
        return 0
    baseline = int(baseline_path.read_text())
    if count > baseline:
        print(f"{count} pyright suppressions, baseline allows {baseline}: remove the new one(s) rather than suppress")
        return 1
    if count < baseline:
        print(f"{count} pyright suppressions, baseline is {baseline}: lower it with `python tools/suppression_ratchet.py --update`")
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
