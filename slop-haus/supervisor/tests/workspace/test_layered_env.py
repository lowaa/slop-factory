from __future__ import annotations

import os
import random
import stat
import string
from collections.abc import Callable
from pathlib import Path

from slop_haus_supervisor.workspace.envfile import EnvFileError
from slop_haus_supervisor.workspace.layered_env import (
    dropped_names,
    is_dropped,
    AgentKind,
    EnvKind,
    Layer,
    LayerFile,
    LayeredEnv,
    LayerRef,
    Resolved,
    known_values,
    listing,
)
from slop_haus_supervisor.workspace.layout import Layout
from support import TempDirTestCase

FILES = list(EnvKind)
BASE, KIND, AGENT = Layer.BASE, Layer.KIND, Layer.AGENT
RC, WORKER = AgentKind.REMOTE_CONTROL, AgentKind.WORKER

# (key is in base, in kind, in agent) -> expected (value, layer) for a worker and for a Remote Control slot,
# written out by hand rather than computed with the precedence rule under test.
TABLE: list[tuple[tuple[bool, bool, bool], tuple[str, Layer] | None, tuple[str, Layer] | None]] = [
    ((False, False, False), None, None),
    ((True, False, False), ("base-v", BASE), ("base-v", BASE)),
    ((False, True, False), None, ("kind-v", KIND)),
    ((False, False, True), ("agent-v", AGENT), ("agent-v", AGENT)),
    ((True, True, False), ("base-v", BASE), ("kind-v", KIND)),
    ((True, False, True), ("agent-v", AGENT), ("agent-v", AGENT)),
    ((False, True, True), ("agent-v", AGENT), ("agent-v", AGENT)),
    ((True, True, True), ("agent-v", AGENT), ("agent-v", AGENT)),
]


class LayerTestCase(TempDirTestCase):
    def setUp(self) -> None:
        super().setUp()
        self.layout = Layout(self.tmp)

    def write(self, ref: LayerRef, file: EnvKind, text: str) -> Path:
        path = ref.path(self.layout, file)
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(text)
        return path

    def clear(self) -> None:
        for ref in (LayerRef(BASE), LayerRef(KIND), LayerRef(AGENT, "a1")):
            for file in FILES:
                ref.path(self.layout, file).unlink(missing_ok=True)

    def put(self, ref: LayerRef, file: EnvKind, **values: str) -> None:
        self.write(ref, file, "".join(f"{k}={v}\n" for k, v in values.items()))

    def got(self, kind: AgentKind, agent: str | None, file: EnvKind, key: str = "KEY") -> tuple[str, Layer] | None:
        found = LayeredEnv(self.layout, kind, agent).resolve(file).get(key)
        return None if found is None else (found.value, found.layer)


class ResolutionTableTest(LayerTestCase):
    def test_every_combination_of_layers_for_every_file(self) -> None:
        for file in FILES:
            for (in_base, in_kind, in_agent), worker, remote in TABLE:
                for kind, expected in ((WORKER, worker), (RC, remote)):
                    with self.subTest(file=file, base=in_base, kind_layer=in_kind, agent=in_agent, agent_kind=kind):
                        self.clear()
                        for present, ref, value in (
                            (in_base, LayerRef(BASE), "base-v"),
                            (in_kind, LayerRef(KIND), "kind-v"),
                            (in_agent, LayerRef(AGENT, "a1"), "agent-v"),
                        ):
                            if present:
                                self.put(ref, file, KEY=value)
                        self.assertEqual(expected, self.got(kind, "a1", file))

    def test_the_files_merge_independently(self) -> None:
        self.put(LayerRef(BASE), EnvKind.RUN, KEY="from-run")
        self.put(LayerRef(AGENT, "a1"), EnvKind.SECRETS, KEY="from-secrets")

        env = LayeredEnv(self.layout, WORKER, "a1")

        self.assertEqual("from-run", env.values(EnvKind.RUN)["KEY"])
        self.assertEqual("from-secrets", env.values(EnvKind.SECRETS)["KEY"])
        self.assertNotIn("KEY", env.values(EnvKind.REPO))

    def test_a_layer_replaces_only_the_keys_it_has(self) -> None:
        self.put(LayerRef(BASE), EnvKind.SECRETS, A="base-a", B="base-b")
        self.put(LayerRef(AGENT, "a1"), EnvKind.SECRETS, B="agent-b")

        self.assertEqual(
            {"A": Resolved("base-a", BASE), "B": Resolved("agent-b", AGENT)},
            LayeredEnv(self.layout, WORKER, "a1").resolve(EnvKind.SECRETS),
        )

    def test_an_agent_layer_may_override_a_shared_login(self) -> None:
        self.put(LayerRef(BASE), EnvKind.SECRETS, CLAUDE_CODE_OAUTH_TOKEN="shared-token")
        self.put(LayerRef(AGENT, "a1"), EnvKind.SECRETS, CLAUDE_CODE_OAUTH_TOKEN="own-token")

        self.assertEqual(("own-token", AGENT), self.got(WORKER, "a1", EnvKind.SECRETS, "CLAUDE_CODE_OAUTH_TOKEN"))
        self.assertEqual(("shared-token", BASE), self.got(WORKER, "a2", EnvKind.SECRETS, "CLAUDE_CODE_OAUTH_TOKEN"))

    def test_a_blank_secret_does_not_blank_a_lower_layer(self) -> None:
        self.put(LayerRef(BASE), EnvKind.SECRETS, KEY="real")
        self.put(LayerRef(AGENT, "a1"), EnvKind.SECRETS, KEY="")

        self.assertEqual(("real", BASE), self.got(WORKER, "a1", EnvKind.SECRETS))

    def test_a_blank_run_value_is_kept(self) -> None:
        self.put(LayerRef(BASE), EnvKind.RUN, KEY="real")
        self.put(LayerRef(AGENT, "a1"), EnvKind.RUN, KEY="")

        self.assertEqual(("", AGENT), self.got(WORKER, "a1", EnvKind.RUN))

    def test_without_the_new_directories_the_result_is_the_base_files(self) -> None:
        self.put(LayerRef(BASE), EnvKind.SECRETS, A="1111", B="2222")

        for kind in AgentKind:
            self.assertEqual({"A": "1111", "B": "2222"}, LayeredEnv(self.layout, kind, "a1").values(EnvKind.SECRETS))

    def test_an_agent_name_that_could_leave_the_root_is_refused(self) -> None:
        for name in ("../x", "a/b", ".hidden"):
            with self.subTest(name=name), self.assertRaises(Exception):
                LayeredEnv(self.layout, WORKER, name)
            with self.subTest(layout=name), self.assertRaises(Exception):
                self.layout.agent_dir(name)


class MaskTest(LayerTestCase):
    def test_the_kind_layer_masks_a_base_key_for_remote_control_only(self) -> None:
        self.put(LayerRef(BASE), EnvKind.SECRETS, WORKER_ONLY="w-secret", KEEP="keep-me")
        self.write(LayerRef(KIND), EnvKind.SECRETS, "!WORKER_ONLY\n")

        self.assertEqual({"KEEP": "keep-me"}, LayeredEnv(self.layout, RC, "r1").values(EnvKind.SECRETS))
        self.assertEqual({"WORKER_ONLY": "w-secret", "KEEP": "keep-me"}, LayeredEnv(self.layout, WORKER, "r1").values(EnvKind.SECRETS))

    def test_an_agent_layer_value_comes_back_through_a_mask(self) -> None:
        self.put(LayerRef(BASE), EnvKind.SECRETS, KEY="base-v")
        self.write(LayerRef(KIND), EnvKind.SECRETS, "!KEY\n")
        self.put(LayerRef(AGENT, "r1"), EnvKind.SECRETS, KEY="agent-v")

        self.assertEqual(("agent-v", AGENT), self.got(RC, "r1", EnvKind.SECRETS))
        self.assertEqual(("base-v", BASE), self.got(WORKER, "r2", EnvKind.SECRETS))

    def test_a_mask_beats_a_value_for_the_same_key_in_the_kind_file(self) -> None:
        self.put(LayerRef(BASE), EnvKind.SECRETS, KEY="base-v")
        self.write(LayerRef(KIND), EnvKind.SECRETS, "KEY=kind-v\n!KEY\n")

        self.assertIsNone(self.got(RC, "r1", EnvKind.SECRETS))

    def test_a_mask_outside_the_kind_layer_is_refused(self) -> None:
        for ref in (LayerRef(BASE), LayerRef(AGENT, "a1")):
            with self.subTest(layer=ref.layer):
                self.clear()
                path = self.write(ref, EnvKind.SECRETS, "OK=fine\n!KEY\n")
                with self.assertRaisesRegex(EnvFileError, f"{path}:2: a mask"):
                    LayeredEnv(self.layout, WORKER, "a1").resolve(EnvKind.SECRETS)

    def test_a_masked_value_is_not_in_the_remote_control_masker_but_is_in_the_workers(self) -> None:
        self.put(LayerRef(BASE), EnvKind.SECRETS, KEY="worker-secret-value")
        self.write(LayerRef(KIND), EnvKind.SECRETS, "!KEY\n")

        self.assertNotIn("worker-secret-value", LayeredEnv(self.layout, RC, "r1").mask_values())
        self.assertIn("worker-secret-value", LayeredEnv(self.layout, WORKER, "w1").mask_values())

    def test_list_shows_a_mask_by_name(self) -> None:
        self.put(LayerRef(BASE), EnvKind.SECRETS, KEY="base-v")
        self.write(LayerRef(KIND), EnvKind.SECRETS, "!KEY\n!OTHER\n")

        shown = [(i.name, i.note) for i in listing(self.layout, LayerRef(KIND), EnvKind.SECRETS)]

        self.assertEqual([("KEY", "masks base"), ("OTHER", "masks nothing in base")], shown)


def isolation_violations(layout: Layout, make: Callable[[AgentKind, str | None], LayeredEnv]) -> list[str]:
    """What a resolver gets wrong about who sees which layer, for the three isolation rules."""
    for ref, value in (
        (LayerRef(BASE), "base-val"),
        (LayerRef(KIND), "rc-only-val"),
        (LayerRef(AGENT, "a1"), "a1-only-val"),
        (LayerRef(AGENT, "a2"), "a2-only-val"),
    ):
        path = ref.path(layout, EnvKind.SECRETS)
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(f"SHARED={value}\n{ref.layer.name}_{ref.agent.upper()}_KEY={value}\n")
    problems: list[str] = []
    worker = make(WORKER, "a2").mask_values()
    if "rc-only-val" in worker:
        problems.append("a worker received the remote control layer")
    if "a1-only-val" in worker:
        problems.append("agent a1's override reached agent a2")
    if "a2-only-val" not in worker:
        problems.append("a2 lost its own layer")
    remote = make(RC, "a1").mask_values()
    if "a2-only-val" in remote:
        problems.append("agent a2's override reached a remote control agent a1")
    if make(WORKER, "a2").values(EnvKind.SECRETS)["SHARED"] != "a2-only-val":
        problems.append("the agent layer did not override the base")
    if make(RC, "a1").values(EnvKind.SECRETS)["SHARED"] != "a1-only-val":
        problems.append("the agent layer did not win over the kind layer")
    if make(RC, None).values(EnvKind.SECRETS)["SHARED"] != "rc-only-val":
        problems.append("the kind layer did not override the base")
    return problems


class _SwappedOrder(LayeredEnv):
    def _layers(self) -> list[LayerRef]:
        return list(reversed(super()._layers()))


class _IgnoresKind(LayeredEnv):
    def _layers(self) -> list[LayerRef]:
        layers = [LayerRef(BASE), LayerRef(KIND)]
        return layers + ([LayerRef(AGENT, self._agent)] if self._agent else [])


class _IgnoresAgentName(LayeredEnv):
    def _layers(self) -> list[LayerRef]:
        return [ref if ref.layer is not AGENT else LayerRef(AGENT, "a1") for ref in super()._layers()]


class IsolationTest(LayerTestCase):
    def test_nothing_crosses_between_kinds_or_agents(self) -> None:
        self.assertEqual([], isolation_violations(self.layout, lambda k, a: LayeredEnv(self.layout, k, a)))

    def test_each_isolation_rule_fails_against_a_resolver_that_breaks_it(self) -> None:
        for broken in (_SwappedOrder, _IgnoresKind, _IgnoresAgentName):
            with self.subTest(resolver=broken.__name__):
                self.setUp()
                problems = isolation_violations(self.layout, lambda k, a, cls=broken: cls(self.layout, k, a))
                self.assertNotEqual([], problems)

    def test_agent_a_override_never_reaches_agent_b_repo_env_or_dot_env(self) -> None:
        self.write(LayerRef(BASE), EnvKind.REPO, "SHARED=shared\n")
        self.write(LayerRef(AGENT, "a1"), EnvKind.REPO, "OWN=own-a1\n")

        a1 = LayeredEnv(self.layout, WORKER, "a1").repo_env_text()
        a2 = LayeredEnv(self.layout, WORKER, "a2").repo_env_text()

        self.assertEqual("SHARED=shared\nOWN=own-a1\n", a1)
        self.assertEqual("SHARED=shared\n", a2)


class RemovalTest(LayerTestCase):
    def test_unset_falls_back_one_layer_at_a_time(self) -> None:
        self.put(LayerRef(BASE), EnvKind.SECRETS, KEY="base-v")
        self.put(LayerRef(KIND), EnvKind.SECRETS, KEY="kind-v")
        self.put(LayerRef(AGENT, "r1"), EnvKind.SECRETS, KEY="agent-v")

        steps: list[tuple[str, Layer] | None] = []
        for ref in (LayerRef(AGENT, "r1"), LayerRef(KIND), LayerRef(BASE)):
            steps.append(self.got(RC, "r1", EnvKind.SECRETS))
            self.assertTrue(LayerFile(self.layout, ref, EnvKind.SECRETS).unset("KEY"))
        steps.append(self.got(RC, "r1", EnvKind.SECRETS))

        self.assertEqual([("agent-v", AGENT), ("kind-v", KIND), ("base-v", BASE), None], steps)

    def test_worker_falls_from_agent_to_base_and_ignores_the_kind_layer(self) -> None:
        self.put(LayerRef(BASE), EnvKind.SECRETS, KEY="base-v")
        self.put(LayerRef(KIND), EnvKind.SECRETS, KEY="kind-v")
        self.put(LayerRef(AGENT, "w1"), EnvKind.SECRETS, KEY="agent-v")

        LayerFile(self.layout, LayerRef(AGENT, "w1"), EnvKind.SECRETS).unset("KEY")

        self.assertEqual(("base-v", BASE), self.got(WORKER, "w1", EnvKind.SECRETS))

    def test_unset_of_an_absent_key_or_file_changes_nothing(self) -> None:
        self.assertFalse(LayerFile(self.layout, LayerRef(AGENT, "x"), EnvKind.SECRETS).unset("KEY"))
        self.assertFalse(self.layout.agent_dir("x").exists())

    def test_unmask_brings_the_base_key_back_for_remote_control(self) -> None:
        self.put(LayerRef(BASE), EnvKind.SECRETS, KEY="base-v")
        kind = LayerFile(self.layout, LayerRef(KIND), EnvKind.SECRETS)
        kind.mask("KEY")
        self.assertIsNone(self.got(RC, None, EnvKind.SECRETS))

        self.assertTrue(kind.unmask("KEY"))

        self.assertEqual(("base-v", BASE), self.got(RC, None, EnvKind.SECRETS))
        self.assertFalse(kind.unmask("KEY"))

    def test_setting_a_value_in_the_kind_layer_lifts_its_mask_and_masking_drops_its_value(self) -> None:
        self.put(LayerRef(BASE), EnvKind.SECRETS, KEY="base-v")
        kind = LayerFile(self.layout, LayerRef(KIND), EnvKind.SECRETS)

        kind.mask("KEY")
        kind.set("KEY", "kind-v")
        self.assertEqual(("kind-v", KIND), self.got(RC, None, EnvKind.SECRETS))
        kind.mask("KEY")
        self.assertIsNone(self.got(RC, None, EnvKind.SECRETS))


FORBIDDEN = ["SLOP_HAUS_TOKEN", "LD_PRELOAD", "DYLD_INSERT_LIBRARIES", "BASH_ENV", "BASH_FUNC_x", "NODE_OPTIONS", "PYTHONPATH"]
ALL_LAYERS = [LayerRef(BASE), LayerRef(KIND), LayerRef(AGENT, "a1")]


class UnknownSettingTest(LayerTestCase):
    def test_an_unknown_name_in_the_base_run_env_is_dropped_and_reported_and_known_names_pass(self) -> None:
        self.write(LayerRef(BASE), EnvKind.RUN, "SLOP_HAUS_FUTURE=on\nSLOP_HAUS_LABELS=a\nOK=fine\n")
        warnings: list[str] = []

        values = LayeredEnv(self.layout, WORKER, "a1", warnings.append).values(EnvKind.RUN)

        self.assertEqual({"SLOP_HAUS_LABELS": "a", "OK": "fine"}, values)
        self.assertEqual(1, len(warnings))
        self.assertIn("SLOP_HAUS_FUTURE isn't known to this supervisor", warnings[0])
        self.assertIn("slop-haus update", warnings[0])
        self.assertRegex(warnings[0], r"\(version [^)]+\)")
        self.assertEqual("ignored: unknown to this supervisor", next(x.note for x in listing(self.layout, LayerRef(BASE), EnvKind.RUN) if x.name == "SLOP_HAUS_FUTURE"))
        self.assertNotIn("SLOP_HAUS_FUTURE", LayeredEnv(self.layout, WORKER, "a1").process_env())
        self.assertEqual(["SLOP_HAUS_FUTURE"], dropped_names(self.layout))

    def test_the_same_name_still_refuses_in_the_kind_and_agent_layers_and_in_other_files(self) -> None:
        for ref, file in [(LayerRef(KIND), EnvKind.RUN), (LayerRef(AGENT, "a1"), EnvKind.RUN), (LayerRef(BASE), EnvKind.SECRETS), (LayerRef(BASE), EnvKind.REPO)]:
            with self.subTest(layer=ref.layer, file=file):
                self.clear()
                path = self.write(ref, file, "OK=fine\nSLOP_HAUS_FUTURE=on\n")
                with self.assertRaises(EnvFileError) as caught:
                    LayeredEnv(self.layout, RC, "a1").resolve(file)
                self.assertTrue(str(caught.exception).startswith(f"{path}:2: refusing"), str(caught.exception))

    def test_a_name_that_controls_execution_still_refuses_in_the_base_run_env(self) -> None:
        self.write(LayerRef(BASE), EnvKind.RUN, "LD_PRELOAD=x\n")
        with self.assertRaisesRegex(EnvFileError, "refusing"):
            LayeredEnv(self.layout, WORKER, "a1").resolve(EnvKind.RUN)

    def test_a_dropped_name_cannot_be_written(self) -> None:
        with self.assertRaisesRegex(EnvFileError, "refusing"):
            LayerFile(self.layout, LayerRef(BASE), EnvKind.RUN).set("SLOP_HAUS_FUTURE", "on")


class RestrictionTest(LayerTestCase):
    def test_a_forbidden_name_is_refused_on_set_at_every_layer_and_nothing_is_written(self) -> None:
        for ref in ALL_LAYERS:
            for file in FILES:
                for name in FORBIDDEN:
                    with self.subTest(layer=ref.layer, file=file, name=name):
                        with self.assertRaisesRegex(EnvFileError, "refusing"):
                            LayerFile(self.layout, ref, file).set(name, "value1")
                        self.assertFalse(ref.path(self.layout, file).exists())

    def test_the_same_error_comes_from_every_layer(self) -> None:
        for name in FORBIDDEN:
            messages: set[str] = set()
            for ref in ALL_LAYERS:
                with self.assertRaises(EnvFileError) as caught:
                    LayerFile(self.layout, ref, EnvKind.SECRETS).set(name, "value1")
                messages.add(str(caught.exception))
            self.assertEqual(1, len(messages), name)

    def test_a_hand_edited_file_is_refused_the_same_way_naming_file_and_line_but_not_the_value(self) -> None:
        for ref in ALL_LAYERS:
            for file in FILES:
                # The base run.env drops unknown SLOP_HAUS_ names instead (UnknownSettingTest).
                for name in [n for n in FORBIDDEN if n.isidentifier() and not (is_dropped(file, ref.layer, n))]:
                    with self.subTest(layer=ref.layer, file=file, name=name):
                        self.clear()
                        path = self.write(ref, file, f"OK=fine\n{name}=distinct-sentinel-9\n")
                        with self.assertRaises(EnvFileError) as caught:
                            LayeredEnv(self.layout, RC, "a1").resolve(file)
                        self.assertTrue(str(caught.exception).startswith(f"{path}:2: refusing"), str(caught.exception))
                        self.assertNotIn("distinct-sentinel-9", str(caught.exception))

    def test_path_is_allowed_in_the_base_run_env_only(self) -> None:
        LayerFile(self.layout, LayerRef(BASE), EnvKind.RUN).set("PATH", "/opt/bin")
        for ref, file in [(r, f) for r in ALL_LAYERS for f in FILES if (r.layer, f) != (BASE, EnvKind.RUN)]:
            with self.subTest(layer=ref.layer, file=file), self.assertRaisesRegex(EnvFileError, "PATH"):
                LayerFile(self.layout, ref, file).set("PATH", "/opt/bin")

    def test_operator_settings_are_run_env_only_at_every_layer(self) -> None:
        for name in ("SLOP_HAUS_LABELS", "SLOP_HAUS_TOKEN_IMPL", "SLOP_HAUS_CODEX_SHARED_LOGIN"):
            for ref in ALL_LAYERS:
                with self.subTest(name=name, layer=ref.layer):
                    LayerFile(self.layout, ref, EnvKind.RUN).set(name, "value1")
                    self.assertEqual("value1", LayeredEnv(self.layout, RC, "a1").values(EnvKind.RUN)[name])
                    for file in (EnvKind.SECRETS, EnvKind.REPO):
                        with self.assertRaisesRegex(EnvFileError, "refusing"):
                            LayerFile(self.layout, ref, file).set(name, "value1")

    def test_a_forbidden_hand_edited_key_can_still_be_unset(self) -> None:
        self.write(LayerRef(AGENT, "a1"), EnvKind.SECRETS, "LD_PRELOAD=x\nOK=fine\n")

        self.assertTrue(LayerFile(self.layout, LayerRef(AGENT, "a1"), EnvKind.SECRETS).unset("LD_PRELOAD"))

        self.assertEqual({"OK": "fine"}, LayeredEnv(self.layout, WORKER, "a1").values(EnvKind.SECRETS))

    def test_a_mask_cannot_be_written_for_a_forbidden_name_or_outside_the_kind_layer(self) -> None:
        with self.assertRaises(EnvFileError):
            LayerFile(self.layout, LayerRef(KIND), EnvKind.SECRETS).mask("LD_PRELOAD")
        with self.assertRaises(EnvFileError):
            LayerFile(self.layout, LayerRef(AGENT, "a1"), EnvKind.SECRETS).mask("KEY")
        with self.assertRaises(EnvFileError):
            LayerFile(self.layout, LayerRef(BASE), EnvKind.SECRETS).unmask("KEY")


class FileHandlingTest(LayerTestCase):
    def test_missing_files_and_directories_are_empty(self) -> None:
        for kind in AgentKind:
            for file in FILES:
                self.assertEqual({}, LayeredEnv(self.layout, kind, "nobody").resolve(file))
        self.assertIsNone(LayeredEnv(self.layout, WORKER, "nobody").repo_env_text())

    def test_malformed_lines_are_skipped_as_the_base_file_always_has(self) -> None:
        for ref in ALL_LAYERS:
            with self.subTest(layer=ref.layer):
                self.write(ref, EnvKind.SECRETS, "nonsense\n1BAD=x\nOPEN=\"never closed\nOK=fine1\n")
                self.assertEqual({"OK": "fine1"}, LayeredEnv(self.layout, RC, "a1").values(EnvKind.SECRETS))

    def test_set_writes_0600_in_a_0700_directory(self) -> None:
        for ref in (LayerRef(KIND), LayerRef(AGENT, "a1")):
            LayerFile(self.layout, ref, EnvKind.SECRETS).set("KEY", "value1")
            path = ref.path(self.layout, EnvKind.SECRETS)
            self.assertEqual(0o600, stat.S_IMODE(path.stat().st_mode))
            self.assertEqual(0o700, stat.S_IMODE(path.parent.stat().st_mode))

    def test_a_world_readable_agent_file_is_read_like_a_world_readable_base(self) -> None:
        base = self.write(LayerRef(BASE), EnvKind.SECRETS, "A=1111\n")
        agent = self.write(LayerRef(AGENT, "x"), EnvKind.SECRETS, "B=2222\n")
        base.chmod(0o644)
        agent.chmod(0o644)

        self.assertEqual({"A": "1111", "B": "2222"}, LayeredEnv(self.layout, WORKER, "x").values(EnvKind.SECRETS))

    def test_a_layer_file_that_links_outside_the_root_is_refused(self) -> None:
        outside = self.tmp / "outside"
        outside.mkdir()
        (outside / "secrets.env").write_text("STOLEN=1111\n")
        root = self.tmp / "root"
        layout = Layout(root)
        (root / "agents").mkdir(parents=True)
        (root / "agents" / "x").symlink_to(outside)

        with self.assertRaisesRegex(EnvFileError, "outside"):
            LayeredEnv(layout, WORKER, "x").resolve(EnvKind.SECRETS)
        (root / "remote-control").symlink_to(outside)
        with self.assertRaisesRegex(EnvFileError, "outside"):
            LayeredEnv(layout, RC).resolve(EnvKind.SECRETS)

    def test_a_base_file_may_still_be_a_link_to_elsewhere(self) -> None:
        (self.tmp / "vault.env").write_text("A=1111\n")
        os.symlink(self.tmp / "vault.env", self.layout.secrets_file)

        self.assertEqual({"A": "1111"}, LayeredEnv(self.layout, WORKER).values(EnvKind.SECRETS))

    def test_the_base_repo_env_is_copied_exactly_until_another_layer_adds_to_it(self) -> None:
        text = "# note\nexport A=1 # c\n\nB='x y'\n"
        self.layout.repo_env_file.write_text(text)
        self.assertEqual(text, LayeredEnv(self.layout, WORKER, "a1").repo_env_text())

        self.write(LayerRef(AGENT, "a1"), EnvKind.REPO, "B=over\n")

        self.assertEqual("A=1\nB=over\n", LayeredEnv(self.layout, WORKER, "a1").repo_env_text())
        self.assertEqual(text, LayeredEnv(self.layout, WORKER, "a2").repo_env_text())

    def test_known_values_cover_every_layer_and_agent_and_survive_a_bad_file(self) -> None:
        self.put(LayerRef(BASE), EnvKind.SECRETS, A="base-val")
        self.put(LayerRef(KIND), EnvKind.SECRETS, A="kind-val")
        self.put(LayerRef(AGENT, "a1"), EnvKind.REPO, A="a1-val")
        self.write(LayerRef(AGENT, "a2"), EnvKind.SECRETS, "LD_PRELOAD=a2-val\n")

        self.assertEqual({"base-val", "kind-val", "a1-val", "a2-val"}, known_values(self.layout))


class ListingTest(LayerTestCase):
    def test_listings_carry_names_and_layers_and_no_values(self) -> None:
        self.put(LayerRef(BASE), EnvKind.SECRETS, A="sentinel-base", B="sentinel-b")
        self.put(LayerRef(KIND), EnvKind.SECRETS, B="sentinel-kind")
        self.put(LayerRef(AGENT, "a1"), EnvKind.SECRETS, A="sentinel-agent")

        kind = listing(self.layout, LayerRef(KIND), EnvKind.SECRETS)
        agent = listing(self.layout, LayerRef(AGENT, "a1"), EnvKind.SECRETS)

        self.assertEqual([("B", KIND, "overrides base")], [(i.name, i.layer, i.note) for i in kind])
        self.assertEqual({("A", AGENT), ("B", BASE)}, {(i.name, i.layer) for i in agent})
        self.assertNotIn("sentinel", repr(kind) + repr(agent))


def _random_layers(rng: random.Random) -> dict[Layer, dict[str, str]]:
    names = [f"K{n}" for n in range(6)]
    return {
        layer: {n: f"{layer.name}-{n}-{rng.randrange(10**6)}" for n in rng.sample(names, rng.randrange(len(names) + 1))}
        for layer in Layer
    }


class PropertyTest(LayerTestCase):
    """A seeded generator, so a failure reproduces from the seed in its message."""

    ROUNDS = 150

    def load(self, layers: dict[Layer, dict[str, str]], file: EnvKind, order: random.Random | None = None) -> None:
        for layer, values in layers.items():
            items = list(values.items())
            if order:
                order.shuffle(items)
            self.write(LayerRef(layer, "a1" if layer is AGENT else ""), file, "".join(f"{k}={v}\n" for k, v in items))

    def test_the_effective_value_is_the_highest_visible_layer_that_defines_it(self) -> None:
        for seed in range(self.ROUNDS):
            rng = random.Random(seed)
            layers = _random_layers(rng)
            file = rng.choice(FILES)
            self.load(layers, file)
            for kind, visible in ((WORKER, (BASE, AGENT)), (RC, (BASE, KIND, AGENT))):
                expected: dict[str, tuple[str, Layer]] = {}
                for layer in visible:
                    for k, v in layers[layer].items():
                        expected[k] = (v, layer)
                got = {k: (r.value, r.layer) for k, r in LayeredEnv(self.layout, kind, "a1").resolve(file).items()}
                self.assertEqual(expected, got, f"seed {seed} {kind}")

    def test_resolution_ignores_key_order_and_unrelated_keys(self) -> None:
        for seed in range(self.ROUNDS):
            rng = random.Random(seed)
            layers = _random_layers(rng)
            self.load(layers, EnvKind.RUN)
            before = LayeredEnv(self.layout, RC, "a1").resolve(EnvKind.RUN)
            self.load(layers, EnvKind.RUN, order=rng)
            self.write(LayerRef(AGENT, "a1"), EnvKind.REPO, "UNRELATED=zzzz\n")
            after = LayeredEnv(self.layout, RC, "a1").resolve(EnvKind.RUN)
            self.assertEqual(before, after, f"seed {seed}")

    def test_set_then_unset_at_a_layer_leaves_the_effective_view_unchanged(self) -> None:
        for seed in range(self.ROUNDS):
            rng = random.Random(seed)
            layers = _random_layers(rng)
            file = rng.choice([EnvKind.SECRETS, EnvKind.RUN])
            self.load(layers, file)
            layer = rng.choice(list(Layer))
            key = "FRESH" + "".join(rng.choices(string.ascii_uppercase, k=4))
            before = {k: LayeredEnv(self.layout, RC, "a1").resolve(file) for k in ("v",)}

            target = LayerFile(self.layout, LayerRef(layer, "a1" if layer is AGENT else ""), file)
            target.set(key, "fresh-value")
            self.assertIn(key, LayeredEnv(self.layout, RC, "a1").resolve(file))
            self.assertTrue(target.unset(key))

            self.assertEqual(before, {k: LayeredEnv(self.layout, RC, "a1").resolve(file) for k in ("v",)}, f"seed {seed}")
