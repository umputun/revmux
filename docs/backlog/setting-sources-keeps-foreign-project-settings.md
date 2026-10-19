---
worth: maybe
where: app/executor/claude.go:Claude.args
added: 2026-10-19
---
# --setting-sources project keeps a foreign PR's settings and drops the user's deny rules

`Claude.args` passes `--setting-sources project`, and the child runs with `cmd.Dir` set to the round's
workdir. When the caller reviews someone else's pull request from a worktree or clone of its head, the
layer kept is that PR's `.claude/settings.json`, which can carry `hooks` and `permissions`, while the
user's own layer, including any permission deny rules he relies on, is the one dropped.

Loading the project layer was already the default before the flag; what the flag adds is losing the user
layer's denials while reviewing a foreign branch. The skill's `pr.md` already defends the prompt tree
against the same shape by resolving `--config-dir` from the main checkout, but not this file.

Unverified: synthesis dropped it as a single-source minor at confidence 55 in
`.revmux/tasks/reviewer-context-flags/01-initial` before verify ran, and nobody has confirmed that
`claude --print` honours a project-layer hook from an untrusted cwd. Surfaced while reviewing
discussion #48.
