import importlib.util
import json
import os
import tempfile
import threading
import unittest
from pathlib import Path
from types import SimpleNamespace


def load_app_module():
    path = Path(__file__).with_name("app.py")
    spec = importlib.util.spec_from_file_location("mempalace_bridge_app", path)
    module = importlib.util.module_from_spec(spec)
    assert spec.loader is not None
    spec.loader.exec_module(module)
    return module


class MemPalaceBridgeTests(unittest.TestCase):
    def setUp(self):
        self.app = load_app_module()
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.previous_data_dir = os.environ.get("MEMPALACE_DATA_DIR")
        self.previous_namespace = os.environ.get("MEMPALACE_NAMESPACE")
        os.environ["MEMPALACE_DATA_DIR"] = self.tmp.name
        os.environ["MEMPALACE_NAMESPACE"] = "testns"
        self.addCleanup(self.restore_env)

    def restore_env(self):
        if self.previous_data_dir is None:
            os.environ.pop("MEMPALACE_DATA_DIR", None)
        else:
            os.environ["MEMPALACE_DATA_DIR"] = self.previous_data_dir
        if self.previous_namespace is None:
            os.environ.pop("MEMPALACE_NAMESPACE", None)
        else:
            os.environ["MEMPALACE_NAMESPACE"] = self.previous_namespace

    def test_health_initializes_workspace_once(self):
        calls = []

        def fake_run_cli(args):
            calls.append(args)
            if args[0] == "init":
                Path(self.tmp.name, "source", "mempalace.yaml").write_text("wing: source\n", encoding="utf-8")
            return SimpleNamespace(returncode=0, stdout="", stderr="")

        self.app.require_mempalace = lambda: None
        self.app.run_cli = fake_run_cli

        self.assertEqual(self.app.health(), {"status": "ok"})
        self.assertEqual(self.app.health(), {"status": "ok"})
        self.assertEqual(calls, [["init", str(Path(self.tmp.name, "source")), "--yes", "--no-llm"]])
        self.assertEqual(Path(self.tmp.name, ".mempalace-ready").read_text(encoding="utf-8"), "ready\n")
        self.assertFalse(Path(self.tmp.name, "palace").exists())

    def test_run_cli_uses_configured_palace_path(self):
        captured = {}

        def fake_run(command, **kwargs):
            captured["command"] = command
            captured["env"] = kwargs["env"]
            return SimpleNamespace(returncode=0, stdout="", stderr="")

        self.app.subprocess.run = fake_run

        self.app.run_cli(["search", "auth"])

        self.assertEqual(
            captured["command"],
            ["mempalace", "--palace", str(Path(self.tmp.name, "palace")), "search", "auth"],
        )
        self.assertEqual(captured["env"]["HOME"], str(Path(self.tmp.name, "home")))

    def test_initialized_health_does_not_wait_for_memory_write(self):
        self.app.require_mempalace = lambda: None
        Path(self.tmp.name, ".mempalace-ready").write_text("ready\n", encoding="utf-8")
        self.app.source_dir().joinpath("mempalace.yaml").write_text("wing: source\n", encoding="utf-8")
        finished = threading.Event()
        results = []

        def probe():
            results.append(self.app.health())
            finished.set()

        with self.app.workspace_lock:
            worker = threading.Thread(target=probe)
            worker.start()
            responsive = finished.wait(1)
        worker.join(2)
        self.assertTrue(responsive, "health blocked behind a normal memory operation")
        self.assertEqual(results, [{"status": "ok"}])

    def test_recall_returns_bounded_history_from_cli(self):
        self.app.require_mempalace = lambda: None

        def fake_run_cli(args):
            if args[0] == "init":
                return SimpleNamespace(returncode=0, stdout="", stderr="")
            if args[0] == "search":
                return SimpleNamespace(returncode=0, stdout="\n".join(f"hit-{i}" for i in range(20)), stderr="")
            raise AssertionError(f"unexpected args {args}")

        self.app.run_cli = fake_run_cli
        out = self.app.recall(self.app.RecallRequest(request={"ProjectID": "p"}, query="auth"))

        self.assertEqual(out["Conventions"], [])
        self.assertEqual(out["Decisions"], [])
        self.assertEqual(out["History"], [f"hit-{i}" for i in range(12)])

    def test_recall_returns_empty_history_before_first_memory_write(self):
        self.app.require_mempalace = lambda: None

        def fake_run_cli(args):
            if args[0] == "init":
                return SimpleNamespace(returncode=0, stdout="", stderr="")
            if args[0] == "search":
                return SimpleNamespace(returncode=1, stdout="", stderr="No palace found at /data/palace")
            raise AssertionError(f"unexpected args {args}")

        self.app.run_cli = fake_run_cli

        out = self.app.recall(self.app.RecallRequest(request={"ProjectID": "p"}, query="auth"))

        self.assertEqual(out["History"], [])

    def test_recall_prefers_query_embedding_vector_hits(self):
        self.app.require_mempalace = lambda: None
        Path(self.tmp.name, ".mempalace-ready").write_text("ready\n", encoding="utf-8")
        Path(self.tmp.name, "palace").mkdir()
        self.app.source_dir().joinpath("mempalace.yaml").write_text("wing: source\n", encoding="utf-8")
        Path(self.tmp.name, "testns.jsonl").write_text(
            "\n".join(
                [
                    json.dumps({"kind": "vector", "text": "auth convention", "embedding": [1.0, 0.0]}),
                    json.dumps({"kind": "vector", "text": "billing convention", "embedding": [0.0, 1.0]}),
                ]
            )
            + "\n",
            encoding="utf-8",
        )

        def fake_run_cli(args):
            if args[0] == "search":
                return SimpleNamespace(returncode=0, stdout="cli fallback", stderr="")
            raise AssertionError(f"unexpected args {args}")

        self.app.run_cli = fake_run_cli
        out = self.app.recall(
            self.app.RecallRequest(request={"ProjectID": "p"}, query="auth", query_embedding=[0.9, 0.1])
        )

        self.assertEqual(out["History"][:2], ["auth convention", "billing convention"])
        self.assertIn("cli fallback", out["History"])

    def test_write_persists_conventions_decisions_vectors_and_mines(self):
        calls = []
        self.app.require_mempalace = lambda: None

        def fake_run_cli(args):
            calls.append(args)
            return SimpleNamespace(returncode=0, stdout="", stderr="")

        self.app.run_cli = fake_run_cli
        payload = self.app.UpdateProposal(
            Conventions=["use bounded HTTP clients"],
            Decisions=["Headroom and MemPalace are required"],
            Vectors=[{"ID": "v1", "Text": "approved final review memory"}],
        )

        self.assertEqual(self.app.write(payload), {"status": "ok"})

        jsonl = Path(self.tmp.name, "testns.jsonl").read_text(encoding="utf-8").splitlines()
        records = [json.loads(line) for line in jsonl]
        self.assertEqual([record["kind"] for record in records], ["convention", "decision", "vector"])
        self.assertIn(
            "Headroom and MemPalace",
            Path(self.tmp.name, "source", "testns-memory.md").read_text(encoding="utf-8"),
        )
        self.assertEqual(
            calls,
            [
                ["init", str(Path(self.tmp.name, "source")), "--yes", "--no-llm"],
                ["mine", str(Path(self.tmp.name, "source"))],
            ],
        )

    def test_write_persists_vector_embedding(self):
        self.app.require_mempalace = lambda: None
        self.app.run_cli = lambda args: SimpleNamespace(returncode=0, stdout="", stderr="")

        payload = self.app.UpdateProposal(Vectors=[{"ID": "v1", "Text": "memory", "Embedding": [0.1, 0.2]}])
        self.assertEqual(self.app.write(payload), {"status": "ok"})

        records = [
            json.loads(line)
            for line in Path(self.tmp.name, "testns.jsonl").read_text(encoding="utf-8").splitlines()
        ]
        self.assertEqual(records[0]["embedding"], [0.1, 0.2])


if __name__ == "__main__":
    unittest.main()
