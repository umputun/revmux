---
worth: yes
where: app/executor/agy.go, app/executor/rollout.go
added: 2026-11-24
---
# agy's silent thinking steps have no liveness signal, so the idle timeout is a ceiling rather than a fix

An agy thinking step writes nothing to either pipe until it is done. A live adversarial review on
`gemini-4-neon:high` had a 129s silent step, so agy runs under a 5m idle timeout while claude and codex
keep 2m. A hung agy agent is therefore noticed only after 5m.

agy's conversation store, `~/.gemini/antigravity-cli/conversations/<conversation_id>.db-wal`, grew every
3-4s through 45s of silent thinking, and `conversation_id` is on the `init` event. A tail that touches
the idle timer whenever that file grows, the way `rollout.go` does for codex through
`runSpec.shareTouch`, would let agy go back to the shared default.

A prompt asking for one question per step was tried instead: it halved the longest silence (129s to
65s) but cost 38% more wall time and tokens, because every extra step resends the whole conversation.

Open before doing it: the path is agy's internal layout and can move between agy versions, so the
tail must degrade to the 5m timeout when the file is absent, and `conversation_id` must be checked
for separators and `..` before it becomes a path.
