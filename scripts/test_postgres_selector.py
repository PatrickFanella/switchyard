import pathlib
import unittest


class PostgresSelectorTest(unittest.TestCase):
    def test_selector_covers_oracle_regression_suites(self) -> None:
        script = pathlib.Path(__file__).with_name("test-postgres.sh").read_text()
        for name in ("Migration", "Affinity", "Adopt", "LockOrder", "StaleAttempt"):
            self.assertIn(name, script)
        self.assertIn("TestPostgres.*", script)
        self.assertIn("TestStoreLifecycleContract/postgres", script)


if __name__ == "__main__":
    unittest.main()
