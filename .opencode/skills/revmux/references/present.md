# Presenting a run — the turn the user answers

Read this when the report is parsed and the turn is about to be written, not before.
It is the whole of presentation for both review shapes.
`SKILL.md` Step 6 and `references/triage.md` section 5 add the recommendation tables and nothing else.

**The turn is a decision brief. It is not the record.**
The record already exists — `report.md` and `findings.json` in the round directory — so nothing is lost
by leaving detail there, and everything is lost by reproducing it in chat: the reader scrolls past the
decision looking for the decision.
Every rule below follows from that sentence.

## The one law of detail

**A body appears in full exactly where the reader acts on it, and nowhere twice.**

In a **code review** the reader acts on each finding — he fixes it, or decides not to — so every
actionable finding carries its body: the trigger, the consequence and the fix.
In a **triage** the reader acts once, on the verdict, so full bodies go only to the arguments the
decision block names.
Every other argument is one line, and the record holds the rest.

Never trim a body you are showing in full.
Never show in full a body the reader will not act on.

## Skeleton: code review

1. **Header** — one to three sentences: what is under review and what it sets out to do.
   For a fetched pull request, whose it is and what its description claims.
2. **Completeness** — expected against reported, `degraded` led with when it is non-empty, and any
   healthy source with `raised: 0` named.
3. **Counts** — one line, by `[surface, severity]` tag.
4. **Findings** — grouped by severity, each headed `[surface, severity] Title — file:line, conf N`,
   then the body's trigger, consequence and fix. Mark any finding with more than one entry in `sources`.
5. **`open_questions`, `pre_existing`, `immaterial`** — their own short sections, one line per entry,
   kept out of the counts.
6. **The decision block, then the question** — below.

## Skeleton: triage

1. **Header** — one line: the item, its title, and panel completeness. Plus the half-sentence saying
   severity here is weight on the decision rather than damage.
2. **The panel** — grouped by agent, severity order within, **one line per argument**:
   `[severity, conf N] title — what it cites`.
   No bodies here. An agent that raised nothing gets a line saying so.
3. **The record** — one line naming where the full arguments are: `<round_dir>/report.md`.
4. **The decision block, then the question** — below.

## The decision block

The last prose in the turn, and **the reader decides from it alone** — everything above it is context he
may not re-read, and on a long turn it is a screen or more away. In order:

- **What is under review** — a small paragraph, three to five sentences: what it is, who filed or wrote
  it, what it claims, and where it stands now.
  Restated here in full even though the header said it. A back-reference is not a restatement.
- **What came back** — the counts and the gist in a line or two: how much there is and what kind.
- **What decides it** — the deciding findings or arguments, **at most three**, each in full: tag, title,
  the reasoning and what it cites.
  Being unable to name which ones decide it means the verdict is not formed yet; form it first.
- **The verdict** — two or three sentences: which way it goes, the point it turns on, and what happens
  next.

## The question

**What the reader needs in order to choose goes inside the question itself.**
He answers from the question, and by then everything above it is off screen or covered by the question's
own rendering. Anything left standing immediately above it is not read.

- the question text opens with the verdict in one sentence, never a bare "what's the call?"
- the recommended answer goes first and is marked, with its reason inside that option's own description
- **an option names what it acts on, never counts it** — "covering the two majors" is unanswerable and
  "covering the `uv` gate in `make test` and the `request_user_input` gap" is not
- a code review recommends by Step 6's outcome table; a triage by the six-answer table in `triage.md`

Ask with the harness's own question tool rather than in prose, and end the turn on it.
Nothing goes after the question.
