# Changelog

## v0.2.1

**Improvements**
- trim reviewer context, and give the loop an exit for an excluded scope #25 @umputun
- scope the confirming-round rule to gating findings a35fe92
- note that the plugin version is maintainer work bf196ea
- bump github.com/stretchr/testify from 1.11.1 to 1.12.1 #17 @dependabot

**Bug Fixes**
- wait before an agent's retry so it does not hit the same window #23 @umputun
- forward CLAUDE_CONFIG_DIR to the overlay child #24 @umputun
- end the rollout liveness case on the tail's own touch 4b87635
- use strings.Cut where only the first field is read 64b4fe3

## v0.2.0

**New Features**
- resolve a project-level `.revmux/profile.md` under the round's own #12 @umputun
- flag redundant code comments 736053b

**Improvements**
- align go comments with the comment rules, fix the treewriter error path #7 @umputun
- surface the skill install beside the binary install, on the site and in the README 0cb124f
- add build badges, a site nav bar and the TUI screenshot to the README 96010bd
- backlog the immaterial gloss and the lens-override panel body bf92e03
- store vendored files without eol conversion 52052d6
- bump github.com/charmbracelet/x/ansi from 0.11.7 to 0.11.8 #11 @dependabot

**Bug Fixes**
- stop closing the tty out from under bubbletea's resize check 774cd46
- name the entry when an escaping link occupies a directory name fb79350
- name the adversarial agent for its lens, not its binary 72263b5
- build the version timestamp with git instead of BSD date 9f33422
- exit the fake CLI through syscall.Exit e01f8ac

## v0.1.0

First release.

revmux runs a structured multi-agent review by spawning and supervising `claude --print` and `codex exec`
subprocesses, then returns findings on stdout as JSON or markdown. It is normally launched by a coding agent
through the shipped skill rather than typed by hand, and the subject can be a change, a plan, a document or a
filed issue.

**The review**

- Three fixed stages: parallel find across a roster of lens agents, one synthesis call that dedupes on
  `(file, line ±2)` and boosts confidence where distinct sources corroborate, then verification grouped so no
  verifier anchors on a neighbour
- A source is a process, never a lens, so an agent carrying two lenses cannot corroborate itself. revmux
  stamps the attribution itself and no schema exposes it to the model
- Supervision per finder: idle and hard timeouts, one automatic retry, and degrade rather than abort with the
  missing source named in the report

**Configuration**

- Eight shipped profiles and thirteen lenses, resolved per file across `./.revmux/`, `~/.config/revmux/` and
  the defaults built into the binary
- One `model:` string per agent or stage selects binary, model and effort together, so claude and codex mix
  inside one review
- A profile composes any number of agents with any lens sets; adding a file under `prompts/profiles/` or
  `lenses/` is all it takes to have your own

**Output and record**

- JSON on stdout by default, markdown with `--markdown`, and exit codes 0, 1 and 2 that callers script
  against
- Task rounds, with every prior round injected into every composed prompt along with an instruction to judge
  it independently
- A run archive per round: composed prompts, verbatim agent output, per-stage findings, an event log of
  stalls and retries, and a manifest recording prompt provenance and requested against actual model
- Terminal UI with a per-process status table, per-agent scrollback and a findings browser, or timestamped
  stderr lines with `--no-tui`

**Around it**

- Five subcommands, all printing JSON: `config`, `new`, `init`, `stats` and `cleanup`
- The caller ships as an agent skill for Claude Code and Codex CLI, with a launcher that runs the TUI in a
  terminal overlay
- Documentation at [revmux.com](https://revmux.com)
