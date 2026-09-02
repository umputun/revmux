#!/usr/bin/env python3
"""Validate the Codex marketplace and its local plugin manifest."""

import json
from pathlib import Path


ROOT = Path(__file__).resolve().parents[2]
MARKETPLACE = ROOT / ".agents" / "plugins" / "marketplace.json"
PLUGIN_ROOT = ROOT / "plugins" / "codex"
EXPECTED_FILES = {
    ".codex-plugin/plugin.json",
    "README.md",
    "skills/revmux/SKILL.md",
    "skills/revmux/agents/openai.yaml",
    "skills/revmux/references/invocation.md",
    "skills/revmux/references/loop.md",
    "skills/revmux/references/output.md",
    "skills/revmux/references/pr.md",
    "skills/revmux/references/present.md",
    "skills/revmux/references/task-dir.md",
    "skills/revmux/references/triage.md",
    "skills/revmux/scripts/agentdeck-window.sh",
    "skills/revmux/scripts/analyze-corpus.py",
    "skills/revmux/scripts/launch-revmux.sh",
    "skills/revmux/scripts/preflight.sh",
    "skills/revmux/scripts/task-state.sh",
}


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
    assert manifest["skills"] == "./skills/"
    assert manifest["interface"]["category"] == plugin["category"]

    skill_text = (PLUGIN_ROOT / "skills" / "revmux" / "SKILL.md").read_text(
        encoding="utf-8"
    )
    assert "~/.codex/skills/revmux" not in skill_text, (
        "Codex skill hard-codes the legacy direct-install path"
    )

    packaged_files = {
        str(path.relative_to(PLUGIN_ROOT))
        for path in PLUGIN_ROOT.rglob("*")
        if path.is_file()
    }
    assert packaged_files == EXPECTED_FILES, (
        f"unexpected Codex payload: {sorted(packaged_files ^ EXPECTED_FILES)}"
    )

    print("Codex marketplace manifest is valid")


if __name__ == "__main__":
    main()
