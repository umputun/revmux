# Codex CLI integration

The **Codex CLI** skill for revmux. Same workflow as the Claude Code plugin, adapted to Codex's
conventions.

## Contents

- `skills/revmux/SKILL.md` — the review skill
- `skills/revmux/references/` — `task-dir.md`, `invocation.md`, `output.md`, `present.md`, `pr.md`,
  `triage.md`, `loop.md`
- `skills/revmux/scripts/` — `preflight.sh`, `task-state.sh`, `launch-revmux.sh`, `analyze-corpus.py`,
  `agentdeck-window.sh`

## Requirements

- `revmux` — `brew install umputun/apps/revmux` on macOS, a binary from the releases page, or
  `go install github.com/umputun/revmux/app@latest` (installs as `app`, rename it)
- `claude` — every lens agent and both model stages run on it by default
- `codex` — needed by any profile, roster entry or stage naming it in a `model:`, which every shipped
  profile except `claude-only` does. `preflight.sh <profile>` answers it for the profile that will run
- `jq` — optional. `preflight.sh` and `task-state.sh` use it when present and fall back without it
- `python3` — for `analyze-corpus.py` only, which self mode runs. Standard library alone, no packages
- a supported terminal, for overlay mode only: agterm, tmux, Zellij, herdr, kitty, wezterm, cmux,
  ghostty, iTerm2, or Emacs vterm

## Install

Add the marketplace and install the plugin:

```bash
codex plugin marketplace add umputun/revmux
codex plugin add revmux@revmux
```

If you previously copied or symlinked the skill into `~/.codex/skills/revmux`, remove that copy after
installing the plugin so the marketplace package is the only one Codex loads.

## `/revmux`

```text
/revmux                    review the current change; scope auto-detected
/revmux this branch        branch versus its base
/revmux last 3 commits     a ref range
/revmux pr 123             fetch the PR into a worktree, review it, clean up
/revmux triage 123         a four-way panel over a filed issue or discussion
/revmux focused            codex peer plus the bugs lens only
/revmux final              the pre-merge profile, nothing below major
/revmux lenses docs,impl   a composed lens set
/revmux watch              run with the TUI in a terminal overlay
```

The skill resolves the scope, opens a round with `revmux new`, writes the context at the paths it
reports, runs revmux, reads the JSON back, and presents the findings. Re-running after fixes is a new
round on the same task; revmux carries the prior rounds into every prompt itself.

## Differences from the Claude Code plugin

- Script paths resolve from the installed skill's absolute path in Codex's available-skills catalogue,
  instead of `$CLAUDE_SKILL_DIR`
- `AskUserQuestion` is replaced by numbered-list prompts, the Codex convention
- `EnterPlanMode` is replaced by an inline markdown plan plus an explicit confirmation before any
  file is modified
- the live progress feed is polled off the running command's own stdout, since Codex has nothing that
  wakes an agent when a file grows; Claude Code arms a `Monitor` on the round's event log instead

Everything else — the round's context files, the flags, the JSON shape, the exit codes, the overlay
launcher and every reference file — is identical between the two.

## Notes

This integration is kept separate from other harnesses on purpose:

- Claude Code integration lives in `.claude-plugin/`
- Codex integration lives here in `plugins/codex/`
