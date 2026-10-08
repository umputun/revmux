---
worth: maybe
where: .claude-plugin/skills/revmux/scripts/launch-revmux.sh
added: 2026-10-07
---
# launch-revmux.sh has no test, and CI cannot see a bash 3.2 defect in it

Nothing runs the launcher. `make lint-scripts` is shellcheck plus the sentinel-helper source check, and
the `build` workflow runs on ubuntu with bash 5. Issue #41 was a bash 3.2 behavior: the ERR trap ran
inside a guarded command substitution and replaced revmux's exit 1 and 2 with the launcher's 3. The
fix is `trap - ERR;` inside that substitution, and removing it again passes every check the repo has.

A test for the agterm path needs no terminal: a fake `agtermctl` that runs the overlay command and
exits with its status, a fake `revmux` that prints a report and exits 0, 1 or 2, both first on `PATH`
with `GHOSTTY_BIN_DIR` and `AGTERM_SESSION_ID` set, and the launcher started as `/bin/bash <script>`.
Expected: the exit code and the report pass through unchanged.

Open before doing it: where a shell test lives in a repo whose tests are all Go, and whether CI gets a
macOS job or a bash 3.2 container, since the test proves nothing under bash 5.
