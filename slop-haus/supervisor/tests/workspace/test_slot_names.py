from __future__ import annotations

import argparse
from pathlib import Path

from slop_haus_supervisor import cli
from slop_haus_supervisor.slophaus.supervisor_api import SlotRef
from slop_haus_supervisor.slophaus.transport import ApiError
from slop_haus_supervisor.workspace import config as cfg
from slop_haus_supervisor.workspace.layout import Layout
from slop_haus_supervisor.workspace.names import SlotNamer, next_number
from support import TempDirTestCase
from test_supervisor_slots import FakeService

CONFIG = cfg.SupervisorConfig("https://s", "tid", "acme")


class NextNumberTest(TempDirTestCase):
    def test_one_past_the_highest_with_the_prefix_and_gaps_stay_empty(self) -> None:
        self.assertEqual(4, next_number("host-", ["host-1", "host-2", "host-3"]))
        self.assertEqual(4, next_number("host-", ["host-1", "host-3"]))
        self.assertEqual(11, next_number("host-", ["host-10", "host-2"]))

    def test_other_prefixes_and_non_numbers_are_ignored(self) -> None:
        self.assertEqual(1, next_number("host-", ["other-5", "host-x", "host-1a", "xhost-9"]))

    def test_the_namer_counts_on_from_each_name_it_hands_out(self) -> None:
        namer = SlotNamer("host", ["host-1"])

        self.assertEqual(["host-2", "host-3"], [namer.next(), namer.next()])


class ServiceWithListing(FakeService):
    retired: tuple[str, ...] = ()

    def taken_names(self) -> set[str]:
        return {*self.slots, *self.retired}


class SlotAddNamesTest(TempDirTestCase):
    def setUp(self) -> None:
        super().setUp()
        self.layout = Layout(self.tmp / "root")
        self.layout.slots.mkdir(parents=True)
        self.layout.config_file.write_text(CONFIG.dumps())
        self.service = ServiceWithListing()
        self.hostname = "box"
        self.patch_hostname()

    def patch_hostname(self) -> None:
        original = cli.short_hostname
        cli.short_hostname = lambda: self.hostname
        self.addCleanup(setattr, cli, "short_hostname", original)

    def add(self, name: str | None = None, count: int = 1) -> None:
        args = argparse.Namespace(name=name, count=count, setup_script=None)
        cli._slot_add(args, self.layout, self.service, {})  # pyright: ignore[reportPrivateUsage]

    def created(self) -> list[str]:
        return [c.split(" ")[1] for c in self.service.calls if c.startswith("create ")]

    def test_the_first_name_is_hostname_1(self) -> None:
        self.add()

        self.assertEqual(["box-1"], self.created())
        self.assertEqual(("box-1",), cfg.load(self.layout.config_file).slots)

    def test_count_adds_that_many(self) -> None:
        self.add(count=3)

        self.assertEqual(["box-1", "box-2", "box-3"], self.created())

    def test_names_taken_in_the_config_on_disk_or_on_the_server_are_skipped(self) -> None:
        self.layout.config_file.write_text(CONFIG.with_slot("box-1").dumps())
        (self.layout.slots / "box-2").mkdir()
        self.service.slots["box-4"] = SlotRef("id", "box-4")

        self.add()

        self.assertEqual(["box-5"], self.created())

    def test_a_retired_slot_with_the_highest_number_still_counts(self) -> None:
        self.service.slots["box-1"] = SlotRef("id1", "box-1")
        self.service.retired = ("box-3",)
        self.add()
        self.assertEqual(["box-4"], self.created())

    def test_a_lone_retired_slot_still_counts(self) -> None:
        self.service.retired = ("box-3",)
        self.add()
        self.assertEqual(["box-4"], self.created())

    def test_a_name_the_server_refuses_as_existing_moves_on_to_the_next(self) -> None:
        # An archived slot keeps its name on the server, where this host cannot see it.
        real = self.service.create_slot

        def create(name: str) -> SlotRef:
            if name == "box-1":
                raise ApiError("the server would not create slot 'box-1': an agent with that name already exists", 409)
            return real(name)

        self.service.create_slot = create

        self.add()

        self.assertEqual(("box-2",), cfg.load(self.layout.config_file).slots)

    def test_a_name_retired_since_the_listing_moves_on_to_the_next(self) -> None:
        real = self.service.create_slot

        def create(name: str) -> SlotRef:
            if name == "box-1":
                raise ApiError("an archived slot of this supervisor holds that name and has history, so it cannot be reused: pick another name", 409)
            return real(name)

        self.service.create_slot = create

        self.add()

        self.assertEqual(("box-2",), cfg.load(self.layout.config_file).slots)

    def test_other_server_refusals_are_not_retried(self) -> None:
        self.service.fail_on["create"] = ApiError("slot cap reached", 409)

        with self.assertRaisesRegex(ApiError, "cap"):
            self.add()

    def test_an_explicit_name_is_used_as_given_and_refuses_count(self) -> None:
        self.add("custom")

        self.assertEqual(["custom"], self.created())
        with self.assertRaises(cfg.ConfigError):
            self.add("again", count=2)

    def test_numbering_is_per_tenant(self) -> None:
        other = Layout(self.tmp / "other")
        other.slots.mkdir(parents=True)
        other.config_file.write_text(cfg.SupervisorConfig("https://s", "tid2", "globex").with_slot("box-7").dumps())
        self.add()

        self.assertEqual(["box-1"], self.created())
        self.assertEqual(Path(other.root).name, "other")
