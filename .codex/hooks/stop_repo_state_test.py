#!/usr/bin/env python3
# Copyright (c) 2026 AUTHORS All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.

"""Tests for stop_repo_state release version helpers."""

from __future__ import annotations

import importlib.util
import sys
import unittest
from pathlib import Path


HOOK_PATH = Path(__file__).with_name("stop_repo_state.py")
SPEC = importlib.util.spec_from_file_location("stop_repo_state", HOOK_PATH)
assert SPEC is not None
stop_repo_state = importlib.util.module_from_spec(SPEC)
assert SPEC.loader is not None
sys.modules["stop_repo_state"] = stop_repo_state
SPEC.loader.exec_module(stop_repo_state)


class ReleaseVersionTests(unittest.TestCase):
    def test_next_release_accepts_patch_minor_and_major_bumps(self) -> None:
        self.assertTrue(stop_repo_state.is_next_release_after("v0.2.10", "v0.2.11"))
        self.assertTrue(stop_repo_state.is_next_release_after("v0.2.10", "v0.3.0"))
        self.assertTrue(stop_repo_state.is_next_release_after("v0.2.10", "v1.0.0"))

    def test_next_release_rejects_skipped_or_malformed_bumps(self) -> None:
        self.assertFalse(stop_repo_state.is_next_release_after("v0.2.10", "v0.2.12"))
        self.assertFalse(stop_repo_state.is_next_release_after("v0.2.10", "v0.3.1"))
        self.assertFalse(stop_repo_state.is_next_release_after("v0.2.10", "v0.4.0"))
        self.assertFalse(stop_repo_state.is_next_release_after("v0.2.10", "v1.1.0"))
        self.assertFalse(stop_repo_state.is_next_release_after("v0.2.10", "v0.2.10"))


if __name__ == "__main__":
    unittest.main()
