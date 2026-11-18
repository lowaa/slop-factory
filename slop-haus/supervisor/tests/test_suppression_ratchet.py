import importlib.util
from pathlib import Path
from types import ModuleType
from unittest import TestCase
import tempfile

# Built piecewise so the ratchet does not count this file's own fixtures.
PYRIGHT = "# pyr" + "ight: ignore"
MYPY = "# ty" + "pe: ignore"

TOOL = Path(__file__).resolve().parent.parent / "tools" / "suppression_ratchet.py"


def load_tool() -> ModuleType:
    spec = importlib.util.spec_from_file_location("suppression_ratchet", TOOL)
    assert spec is not None and spec.loader is not None
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


class SuppressionRatchetTest(TestCase):
    def setUp(self) -> None:
        self.tool = load_tool()
        self._tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self._tmp.cleanup)
        self.root = Path(self._tmp.name)
        (self.root / "src").mkdir()
        (self.root / "tests").mkdir()
        self.baseline = self.root / "baseline.txt"

    def run_tool(self, source: str, baseline: int, *argv: str) -> int:
        (self.root / "src" / "a.py").write_text(source)
        self.baseline.write_text(f"{baseline}\n")
        return self.tool.main(list(argv), self.root, self.baseline)

    def test_count_at_baseline_passes(self) -> None:
        self.assertEqual(0, self.run_tool(f"x = 1  {PYRIGHT}[reportFoo]\n", 1))

    def test_new_suppression_above_baseline_fails(self) -> None:
        self.assertEqual(1, self.run_tool(f"x = 1  {PYRIGHT}[reportFoo]\n", 0))

    def test_count_below_baseline_fails_until_lowered(self) -> None:
        self.assertEqual(1, self.run_tool("x = 1\n", 1))

    def test_bare_and_mypy_style_ignores_fail(self) -> None:
        self.assertEqual(1, self.run_tool(f"x = 1  {PYRIGHT}\n", 1))
        self.assertEqual(1, self.run_tool(f"x = 1  {MYPY}[arg-type]\n", 1))

    def test_update_rewrites_baseline(self) -> None:
        self.assertEqual(0, self.run_tool(f"x = 1  {PYRIGHT}[reportFoo]\n", 5, "--update"))
        self.assertEqual("1\n", self.baseline.read_text())
