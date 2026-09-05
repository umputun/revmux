#!/usr/bin/env python3
"""Validate the Codex marketplace and its local plugin manifest."""

import json
from pathlib import Path


ROOT = Path(__file__).resolve().parents[2]
MARKETPLACE = ROOT / ".agents" / "plugins" / "marketplace.json"
CLAUDE_MARKETPLACE = ROOT / ".claude-plugin" / "marketplace.json"
PLUGIN_ROOT = ROOT / "plugins" / "codex"
# entry files nothing else checks: the manifest and SKILL.md fail their own reads above, and
# `make check-plugins` diffs references/ and scripts/ against the Claude tree
ENTRY_FILES = (
    "README.md",
    "skills/revmux/agents/openai.yaml",
)


def load_json(path: Path) -> dict:
    with path.open(encoding="utf-8") as stream:
        return json.load(stream)


def main() -> None:
    if not __debug__:
        raise RuntimeError("assertions must be enabled for marketplace validation")

    marketplace = load_json(MARKETPLACE)
    assert marketplace["name"] == "revmux"
    assert marketplace["interface"]["displayName"] == "Revmux"
    assert len(marketplace["plugins"]) == 1, "unexpected Codex plugin count"

    plugin = marketplace["plugins"][0]
    assert set(plugin) == {"name", "source", "policy", "category"}
    assert plugin["name"] == "revmux"
    assert plugin["source"] == {
        "source": "local",
        "path": "./plugins/codex",
    }
    assert plugin["policy"] == {
        "installation": "AVAILABLE",
        "authentication": "ON_INSTALL",
    }
    assert plugin["category"] == "Developer Tools"

    resolved_root = (ROOT / plugin["source"]["path"]).resolve()
    assert resolved_root == PLUGIN_ROOT.resolve(), "Codex source points outside its plugin root"

    manifest = load_json(PLUGIN_ROOT / ".codex-plugin" / "plugin.json")
    claude_manifest = load_json(ROOT / ".claude-plugin" / "plugin.json")
    assert manifest["name"] == plugin["name"]
    assert manifest["version"] == claude_manifest["version"], (
        "Codex and Claude plugin versions differ"
    )

    # the version is stated in three files and a bump that misses one is invisible otherwise
    claude_marketplace = load_json(CLAUDE_MARKETPLACE)
    claude_entries = [
        entry for entry in claude_marketplace["plugins"] if entry["name"] == manifest["name"]
    ]
    assert len(claude_entries) == 1, "revmux is not a single entry in the Claude marketplace"
    assert claude_entries[0]["version"] == claude_manifest["version"], (
        "Claude marketplace and plugin manifest versions differ"
    )
    assert manifest["skills"] == "./skills/"
    assert manifest["interface"]["category"] == plugin["category"]

    skill_text = (PLUGIN_ROOT / "skills" / "revmux" / "SKILL.md").read_text(
        encoding="utf-8"
    )
    assert "~/.codex/skills/revmux" not in skill_text, (
        "Codex skill hard-codes the legacy direct-install path"
    )

    for relative in ENTRY_FILES:
        assert (PLUGIN_ROOT / relative).is_file(), f"Codex payload is missing {relative}"

    print("Codex marketplace manifest is valid")


if __name__ == "__main__":
    main()
