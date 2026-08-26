"""Pure helper tests for the opt-in pipeline-label eval."""

import subprocess
import unittest
from pathlib import Path
from unittest.mock import patch

from evals.pipeline_labels import (
    EvalFailure,
    assert_label_lifecycle,
    assert_run_result,
    cleanup,
    ensure_branch_absent,
    owned_branch,
    pull_request_url,
    worktree_for_branch,
)


def transitions(*labels: str) -> tuple[tuple[str, str], ...]:
    events: list[tuple[str, str]] = []
    for previous, label in zip((None, *labels), labels):
        if previous is not None:
            events.append(("unlabeled", previous))
        events.append(("labeled", label))
    return tuple(events)


class LabelLifecycleTests(unittest.TestCase):
    def test_accepts_expected_lifecycle(self) -> None:
        assert_label_lifecycle(
            ("factory:ready-for-review",),
            transitions(
                "factory:planning",
                "factory:building",
                "factory:verifying",
                "factory:verifying",
                "factory:ready-for-review",
            ),
            0,
        )

    def test_rejects_missing_transition(self) -> None:
        with self.assertRaisesRegex(EvalFailure, "expected planning"):
            assert_label_lifecycle(
                ("factory:ready-for-review",),
                transitions(
                    "factory:planning",
                    "factory:verifying",
                    "factory:ready-for-review",
                ),
                0,
            )

    def test_rejects_conflicting_final_label(self) -> None:
        with self.assertRaisesRegex(EvalFailure, "expected only"):
            assert_label_lifecycle(
                ("factory:ready-for-review", "factory:blocked"),
                transitions(
                    "factory:planning",
                    "factory:building",
                    "factory:verifying",
                    "factory:ready-for-review",
                ),
                0,
            )

    def test_rejects_nonzero_factory_exit(self) -> None:
        with self.assertRaisesRegex(EvalFailure, "status 2"):
            assert_label_lifecycle((), (), 2)

    def test_accepts_repair_cycle(self) -> None:
        assert_label_lifecycle(
            ("factory:ready-for-review",),
            transitions(
                "factory:planning",
                "factory:building",
                "factory:verifying",
                "factory:building",
                "factory:verifying",
                "factory:ready-for-review",
            ),
            0,
        )

    def test_rejects_early_ready_transition(self) -> None:
        with self.assertRaisesRegex(EvalFailure, "expected planning"):
            assert_label_lifecycle(
                ("factory:ready-for-review",),
                transitions(
                    "factory:planning",
                    "factory:ready-for-review",
                    "factory:building",
                    "factory:verifying",
                    "factory:ready-for-review",
                ),
                0,
            )

    def test_rejects_exception_transition(self) -> None:
        with self.assertRaisesRegex(EvalFailure, "expected planning"):
            assert_label_lifecycle(
                ("factory:ready-for-review",),
                transitions(
                    "factory:planning",
                    "factory:blocked",
                    "factory:building",
                    "factory:verifying",
                    "factory:ready-for-review",
                ),
                0,
            )

    def test_rejects_backward_transition(self) -> None:
        with self.assertRaisesRegex(EvalFailure, "expected planning"):
            assert_label_lifecycle(
                ("factory:ready-for-review",),
                transitions(
                    "factory:planning",
                    "factory:building",
                    "factory:planning",
                    "factory:verifying",
                    "factory:ready-for-review",
                ),
                0,
            )

    def test_rejects_success_without_owned_pull_request(self) -> None:
        with self.assertRaisesRegex(EvalFailure, "owned pull request"):
            assert_run_result(
                ("factory:ready-for-review",),
                transitions(
                    "factory:planning",
                    "factory:building",
                    "factory:verifying",
                    "factory:ready-for-review",
                ),
                0,
                None,
            )

    def test_rejects_accumulated_factory_labels(self) -> None:
        with self.assertRaisesRegex(EvalFailure, "one active Factory label"):
            assert_label_lifecycle(
                ("factory:ready-for-review",),
                (
                    ("labeled", "factory:planning"),
                    ("labeled", "factory:building"),
                    ("labeled", "factory:verifying"),
                    ("unlabeled", "factory:planning"),
                    ("unlabeled", "factory:building"),
                    ("unlabeled", "factory:verifying"),
                    ("labeled", "factory:ready-for-review"),
                ),
                0,
            )


class EvidenceTests(unittest.TestCase):
    def test_finds_marked_pull_request(self) -> None:
        self.assertEqual(
            pull_request_url(
                (
                    {
                        "body": "<!-- factory:foreman-pr -->\n"
                        "https://github.com/acme/factory-evals/pull/9"
                    },
                ),
                (),
                "acme/factory-evals",
            ),
            "https://github.com/acme/factory-evals/pull/9",
        )

    def test_falls_back_to_cross_reference(self) -> None:
        self.assertEqual(
            pull_request_url(
                (),
                (
                    {
                        "event": "cross-referenced",
                        "source": {
                            "issue": {
                                "html_url": "https://github.com/acme/factory-evals/pull/9",
                                "pull_request": {},
                            }
                        },
                    },
                ),
                "acme/factory-evals",
            ),
            "https://github.com/acme/factory-evals/pull/9",
        )

    def test_rejects_pull_request_from_another_repository(self) -> None:
        self.assertIsNone(
            pull_request_url(
                (
                    {
                        "body": "<!-- factory:foreman-pr -->\n"
                        "https://github.com/acme/production/pull/9"
                    },
                ),
                (),
                "acme/factory-evals",
            )
        )

    def test_finds_exact_branch_worktree(self) -> None:
        output = """worktree /code/factory
HEAD abc123
branch refs/heads/main

worktree /code/.worktrees/factory/eval
HEAD def456
branch refs/heads/codex/eval-marker

"""
        self.assertEqual(
            worktree_for_branch(output, "codex/eval-marker"),
            Path("/code/.worktrees/factory/eval"),
        )

    def test_accepts_exact_same_repository_eval_branch(self) -> None:
        self.assertEqual(
            owned_branch(
                {
                    "headRefName": "codex/factory-eval-run-12",
                    "isCrossRepository": False,
                },
                "run-12",
            ),
            "codex/factory-eval-run-12",
        )

    def test_rejects_unrelated_codex_branch(self) -> None:
        with self.assertRaisesRegex(EvalFailure, "unexpected eval branch"):
            owned_branch(
                {"headRefName": "codex/unrelated", "isCrossRepository": False},
                "run-12",
            )

    def test_rejects_fork_branch(self) -> None:
        with self.assertRaisesRegex(EvalFailure, "unexpected eval branch"):
            owned_branch(
                {
                    "headRefName": "codex/factory-eval-run-12",
                    "isCrossRepository": True,
                },
                "run-12",
            )


class CleanupTests(unittest.TestCase):
    @patch("evals.pipeline_labels.remove_owned_branch")
    @patch("evals.pipeline_labels.command")
    def test_cleans_owned_branch_without_pull_request(
        self, mocked_command, mocked_remove
    ) -> None:
        mocked_command.return_value = subprocess.CompletedProcess([], 0, "", "")

        errors = cleanup(
            "acme/factory-evals",
            Path("/code/factory-evals"),
            "https://github.com/acme/factory-evals/issues/12",
            None,
            "run-12",
        )

        self.assertEqual(errors, [])
        mocked_remove.assert_called_once_with(
            "acme/factory-evals",
            Path("/code/factory-evals"),
            "codex/factory-eval-run-12",
        )

    @patch("evals.pipeline_labels.command")
    def test_refuses_preexisting_local_branch(self, mocked_command) -> None:
        mocked_command.return_value = subprocess.CompletedProcess([], 0, "", "")

        with self.assertRaisesRegex(EvalFailure, "already exists locally"):
            ensure_branch_absent(
                Path("/code/factory-evals"), "codex/factory-eval-run-12"
            )

    @patch("evals.pipeline_labels.command")
    def test_accepts_absent_local_and_remote_branch(self, mocked_command) -> None:
        mocked_command.side_effect = (
            subprocess.CompletedProcess([], 1, "", ""),
            subprocess.CompletedProcess([], 2, "", ""),
        )

        ensure_branch_absent(
            Path("/code/factory-evals"), "codex/factory-eval-run-12"
        )


if __name__ == "__main__":
    unittest.main()
