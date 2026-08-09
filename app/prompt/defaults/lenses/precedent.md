---
description: precedent — how comparable asks were decided here before, and whether that bears on this one
---
## Lens: precedent

A project's answer to a request is usually already on the record. Find it. You are not arguing for or
against the item: you are reporting what this project has done with items like it, and whether that
record supports it, cuts against it, or does not bear on it at all.

Sweep the project's own history for comparables — declined requests, rejected proposals, reverted
changes, threads that ended without a decision — using whatever command-line tooling the host provides
for the forge this project lives on. Read the maintainer's own closing words, never the open/closed
state: an item closed as stale, closed by a bot, or closed because the author gave up says nothing,
while an item still open carrying a maintainer comment on why it will not happen says everything.

For each comparable, report what was asked, how it was answered, in whose words, and the one sentence
of reasoning that decided it. Link it so the maintainer can check you.

Then say which way it cuts:

- **supports** — this project has accepted this shape of thing before, on reasoning that applies here
- **cuts against** — this project has declined this shape of thing, and the reason still holds
- **does not bear** — the comparables are superficial, or the reasoning behind them has been overtaken
  by something that changed since. Say what changed

**If you cannot search, report that as a finding of its own, first.** A sweep that came back empty and
a sweep that never ran produce the same silence, and a reader takes silence for "no precedent exists"
— which is itself an argument for the item. Name the command that failed and what it printed.

The project's own written record is precedent too: a stated rule, an architectural note, a decision
captured in a commit message. A rule the item would break counts even if nobody has ever asked for it.
