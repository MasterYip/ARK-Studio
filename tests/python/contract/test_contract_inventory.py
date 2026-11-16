"""The Go-port contract inventory must follow the Python server it describes."""
from __future__ import annotations

import importlib.util
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[3]


def _load_generator():
    spec = importlib.util.spec_from_file_location(
        "ark_contract_gen", ROOT / "tests" / "python" / "contract" / "gen_contract.py")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


class ContractInventoryTest(unittest.TestCase):
    def test_contract_files_are_current(self):
        gen = _load_generator()
        self.assertEqual(gen.main(["--check"]), 0,
                         "docs/development/go-port/contract.* is stale: run "
                         "python -B tests/python/contract/gen_contract.py")

    def test_go_schema_copy_is_current(self):
        spec = importlib.util.spec_from_file_location(
            "ark_schema_gen", ROOT / "tests" / "python" / "contract" / "gen_schema.py")
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
        self.assertEqual(module.main(["--check"]), 0,
                         "go/internal/store/schema_gen.go is stale: run "
                         "python -B tests/python/contract/gen_schema.py")

    def test_go_static_snapshot_is_current(self):
        spec = importlib.util.spec_from_file_location(
            "ark_static_gen", ROOT / "tests" / "python" / "contract" / "gen_static.py")
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
        self.assertEqual(module.main(["--check"]), 0,
                         "go/internal/server/static_gen.go is stale: run "
                         "python -B tests/python/contract/gen_static.py")

    def test_go_seed_recordings_are_current(self):
        spec = importlib.util.spec_from_file_location(
            "ark_seeds_gen", ROOT / "tests" / "python" / "contract" / "gen_seeds.py")
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
        self.assertEqual(module.main(["--check"]), 0,
                         "go/internal/project/seeds_gen.go is stale: run "
                         "python -B tests/python/contract/gen_seeds.py")

    def test_go_skill_assets_are_current(self):
        spec = importlib.util.spec_from_file_location(
            "ark_skillkit_gen", ROOT / "tests" / "python" / "contract" / "gen_skillkit.py")
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
        self.assertEqual(module.main(["--check"]), 0,
                         "go/internal/skillkit/assets_gen.go or go/internal/pytext/upper_gen.go is stale: "
                         "run python -B tests/python/contract/gen_skillkit.py")

    def test_route_tables_are_current(self):
        spec = importlib.util.spec_from_file_location(
            "ark_route_table", ROOT / "tests" / "python" / "contract" / "route_table.py")
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
        self.assertEqual(module.main(["--check"]), 0,
                         "docs/development/go-port/routes.md or README.md is stale: run "
                         "python -B tests/python/contract/route_table.py")

    def test_go_reads_every_config_key_and_env_var(self):
        spec = importlib.util.spec_from_file_location(
            "ark_config_parity", ROOT / "tests" / "python" / "contract" / "config_parity.py")
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
        self.assertEqual(module.main(["--check"]), 0,
                         "docs/development/go-port/config-parity.md is stale or a config key / env var "
                         "has no Go reader: run python -B tests/python/contract/config_parity.py")

    def test_inventory_finds_core_routes(self):
        gen = _load_generator()
        ids = {(r["id"], tuple(r["methods"])) for r in gen.build()["routes"]}
        for expected in [("exact:/api/health", ("GET", "HEAD")),
                         ("exact:/api/graph/snapshot", ("GET", "HEAD")),
                         (r"regex:^/api/units/([^/]+)$", ("PATCH",)),
                         ("exact:/login", ("POST",))]:
            self.assertIn(expected, ids)


if __name__ == "__main__":
    unittest.main()
