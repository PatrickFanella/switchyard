import json
import os
import tempfile
import unittest
import subprocess
from pathlib import Path
from unittest.mock import patch

from reconcile_open_pilot_repositories import discover, select_pool, private_directory, Client, activate, retain_executor_image


def repository(identifier, slug, **extra):
    return {"id": identifier, "full_name": slug, "has_issues": True, "default_branch": "master",
            "permissions": {"push": True}, **extra}


def issue(slug, number, labels=(), assignees=()):
    return {"repository": {"full_name": slug}, "number": number, "state": "open",
            "labels": [{"name": x} for x in labels], "assignees": [{"login": x} for x in assignees]}


class Upstream:
    def __init__(self, repositories, labeled=(), assigned=()):
        self.repositories, self.labeled, self.assigned = repositories, list(labeled), list(assigned)

    def pages(self, path, parameters, key=None):
        if path.endswith("repos/search"):
            return self.repositories
        return self.assigned if parameters.get("assigned") else self.labeled

    def request(self, path):
        return {"login": "open-pilot"}


class RepositoryReconciliationTests(unittest.TestCase):
    def test_image_retention_checks_exact_local_identity_before_tagging(self):
        image = "sha256:" + "a" * 64
        with patch("reconcile_open_pilot_repositories.subprocess.run",
                   return_value=subprocess.CompletedProcess([], 0, stdout=image + "\n")) as run:
            retain_executor_image(image, Path("/deployment/deploy/docker-local"))
            self.assertEqual(run.call_count, 2)
            self.assertEqual(run.call_args.args[0][-1], "switchyard-open-pilot-pinned:" + "a" * 64)
        with patch("reconcile_open_pilot_repositories.subprocess.run",
                   return_value=subprocess.CompletedProcess([], 0, stdout="sha256:" + "b" * 64)) as run:
            with self.assertRaisesRegex(ValueError, "exact immutable"):
                retain_executor_image(image, Path("/deployment/deploy/docker-local"))
            self.assertEqual(run.call_count, 1)

    def test_future_repository_and_queue_rules(self):
        upstream = Upstream([repository(1, "owner/first")],
                            [issue("owner/first", 1, ["ready", "queued"])])
        first = discover(upstream, ["ready", "queued"], "open-pilot")
        self.assertEqual(first[0]["issues"], [1])
        upstream.repositories.append(repository(2, "owner/future"))
        upstream.labeled += [issue("owner/future", 2, ["ready"]),
                             issue("owner/future", 3, ["ready", "queued"])]
        upstream.assigned += [issue("owner/future", 4, assignees=["OPEN-PILOT"]),
                              issue("owner/future", 3, ["ready", "queued"])]
        second = discover(upstream, ["ready", "queued"], "open-pilot")
        self.assertEqual(second[1]["issues"], [3, 4])
        self.assertEqual(second[1]["default_branch"], "master")

    def test_catalog_exceeds_active_registry_limit(self):
        catalog = discover(Upstream([repository(x, f"owner/repo{x}") for x in range(1, 178)]), [], "")
        self.assertEqual(len(catalog), 177)

    def test_blocked_repositories_remain_visible(self):
        upstream = Upstream([repository(1, "owner/archived", archived=True),
                             repository(2, "owner/mirror", mirror=True),
                             repository(3, "owner/readonly", permissions={"push": False}),
                             repository(4, "owner/empty", empty=True)])
        self.assertEqual({x["blocker"] for x in discover(upstream, [], "")},
                         {"archived", "read_only_mirror", "write_access_missing", "empty_repository"})

    def test_pool_protects_pinned_and_resumable_identity(self):
        registry = {"repositories": [{"id": "pin", "slug": "owner/pin"},
                                     {"id": "paused", "slug": "owner/paused"},
                                     {"id": "done", "slug": "owner/done"}]}
        catalog = [{"gitea_id": x, "slug": f"owner/new{x}", "issues": [1], "blocker": ""} for x in range(1, 5)]
        states = [{"repository_id": "paused", "phase": "paused"},
                  {"repository_id": "done", "phase": "issue_closed"}]
        keep, selected, pending, retired = select_pool(registry, catalog, states, {"pin"}, 3)
        self.assertEqual([x["id"] for x in keep], ["pin", "paused"])
        self.assertEqual([x["gitea_id"] for x in selected], [1])
        self.assertEqual(len(pending), 3)
        self.assertEqual([x["id"] for x in retired], ["done"])
        self.assertEqual(registry["repositories"][2]["id"], "done")

    def test_pool_does_not_evict_unfinished_work_even_above_capacity(self):
        registry = {"repositories": [{"id": str(x), "slug": f"owner/r{x}"} for x in range(5)]}
        states = [{"repository_id": str(x), "phase": "failed"} for x in range(5)]
        catalog = [{"gitea_id": 99, "slug": "owner/new", "issues": [1], "blocker": ""}]
        keep, selected, pending, retired = select_pool(registry, catalog, states, set(), 2)
        self.assertEqual(len(keep), 5)
        self.assertEqual(selected, [])
        self.assertEqual(len(pending), 1)
        self.assertEqual(retired, [])

    def test_duplicate_upstream_identity_fails_closed(self):
        with self.assertRaises(ValueError):
            discover(Upstream([repository(1, "owner/a"), repository(1, "owner/b")]), [], "")

    def test_private_root_rejects_symlink_and_public_permissions(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            (root / "link").symlink_to(root, target_is_directory=True)
            with self.assertRaises(ValueError):
                private_directory(root / "link")
            (root / "public").mkdir(mode=0o755)
            with self.assertRaises(ValueError):
                private_directory(root / "public")
            private_directory(root / "private")
            self.assertEqual((root / "private").stat().st_mode & 0o777, 0o700)

    def test_authenticated_client_rejects_remote_http_and_url_credentials(self):
        for base in ("http://example.test", "https://token@example.test", "https://example.test?secret=x"):
            with self.assertRaises(ValueError):
                Client(base, "secret", "token")

    def test_page_collection_fetches_next_page_and_rejects_bad_payload(self):
        client = Client("https://example.test", "secret", "token")
        with patch.object(client, "request", side_effect=[{"data": list(range(50))}, {"data": [50]}]) as request:
            self.assertEqual(client.pages("/repos", {}, "data"), list(range(51)))
            self.assertIn("page=2", request.call_args.args[0])
        with patch.object(client, "request", return_value={"data": {}}):
            with self.assertRaises(ValueError):
                client.pages("/repos", {}, "data")

    def test_activation_preserves_operator_pause(self):
        class API:
            def request(self, path):
                return {"settings": [{"key": "scheduler.dispatch_paused", "effective": "true"},
                                     {"key": "workers.claim_paused", "effective": "false"}]}
        with patch("reconcile_open_pilot_repositories.subprocess.run") as run:
            with self.assertRaisesRegex(ValueError, "operator runtime pause"):
                activate(API(), {}, {}, {}, Path("/unused"), Path("/unused"))
            run.assert_not_called()


if __name__ == "__main__":
    unittest.main()
