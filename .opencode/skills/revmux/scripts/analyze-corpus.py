#!/usr/bin/env python3
"""analyze-corpus.py - read a revmux run archive and report what it says about the review itself.

Self mode's job is to hand the user conclusions, not numbers. Every measurement below was worked out by
hand once, and doing that again per session is slow and gets it wrong: three of the conclusions reached
that way were retracted, each because a number meant something other than it looked like. They are
encoded here so the analysis starts from the corrected reading.

It opens no round, writes nothing, and runs no model. Safe at any time, including during a review.

usage:
    analyze-corpus.py [--tasks-dir DIR] [--json] [--test]

options:
    --tasks-dir   the tasks root; default is what `revmux config` reports
    --json        the full measurements, for a caller that wants to reason further
    --test        run unit tests
"""

import argparse
import json
import subprocess
import sys
from collections import defaultdict
from pathlib import Path
from typing import Any

# a finding above this is what gates a loop and what a user acts on; below it is the background rate
GATING = ("critical", "major")

ARRAYS = ("findings", "open_questions", "pre_existing", "immaterial")

# ordered so a change of severity can be read as a direction. Anything unrecognised ranks with minor:
# inventing a rank for it would report a direction nothing established
SEVERITY_RANK = {"critical": 3, "major": 2, "minor": 1}


def rank(severity: Any) -> int:
    """Where a severity sits in the order, for reading a change as a direction rather than a pair."""
    return SEVERITY_RANK.get(str(severity), 1)


def findings_of(report: dict[str, Any]) -> list[dict[str, Any]]:
    """Every finding a stage report carries, across all four of its arrays.

    A report splits its findings by what became of them, so any count taken from `findings` alone moves
    when verification reclassifies one — which is a stage doing its job, not attrition.
    """
    out: list[dict[str, Any]] = []
    for key in ARRAYS:
        out.extend(report.get(key) or [])
    return out


def load(path: Path) -> dict[str, Any] | None:
    """Read one JSON artifact, or None when it is absent or will not decode.

    An interrupted run leaves exactly that, and a round missing one snapshot still says something through
    the others.
    """
    try:
        with path.open(encoding="utf-8") as fh:
            data = json.load(fh)
        return data if isinstance(data, dict) else None
    except (OSError, json.JSONDecodeError):
        return None


class Corpus:
    """Every round under a tasks root, and what the stages did to the findings passing through them."""

    def __init__(self, tasks_dir: Path) -> None:
        self.tasks_dir = tasks_dir
        self.rounds: list[dict[str, Any]] = []
        self.unreadable: list[str] = []

    def scan(self) -> None:
        """Walk every round directory, newest task last, and measure each one."""
        for task in sorted(p for p in self.tasks_dir.glob("*") if p.is_dir()):
            for rnd in sorted(p for p in task.glob("*") if p.is_dir()):
                if not self.has_run(rnd):
                    continue
                measured = self.measure(task.name, rnd.name, rnd)
                if measured is None:
                    self.unreadable.append(f"{task.name}/{rnd.name}")
                    continue
                self.rounds.append(measured)

    def has_run(self, rnd: Path) -> bool:
        """Whether revmux counts this directory as a round, by the same test `task.HasRun` applies.

        The marker is created empty when a run claims the round and filled in when it finishes, so its
        mere presence means claimed rather than completed. Counting those would put a round nobody
        reviewed into the denominator of every rate below, and would disagree with the round count
        `revmux stats` and `revmux config` report for the same task.
        """
        marker = rnd / "manifest.json"
        try:
            return marker.is_file() and marker.stat().st_size > 0
        except OSError:
            return False

    def measure(self, task: str, run: str, path: Path) -> dict[str, Any] | None:
        """One round: what each stage received, what it passed on, and how severity moved."""
        found = load(path / "stages" / "1-found.json")
        synth = load(path / "stages" / "2-synthesized.json")
        verified = load(path / "stages" / "3-verified.json")
        report = load(path / "findings.json")
        if found is None:
            return None

        out: dict[str, Any] = {
            "task": task,
            "run": run,
            "raised": len(findings_of(found)),
            "by_lens": self.by_lens(found),
            "lens_naming": self.lens_naming(found),
        }
        if report is not None:
            actionable = report.get("findings") or []
            out["gating"] = sum(1 for f in actionable if f.get("severity") in GATING)
            out["minor"] = sum(1 for f in actionable if f.get("severity") == "minor")
        if synth is not None:
            # removal only. Synthesis keeps the highest severity of everything it merged and files the
            # result under merged_ids[0] alone (app/pipeline/synthesize.go), discarding the other ids,
            # so comparing the merged severity against that one input reports the preserved maximum as
            # a promotion. There is no baseline in the artifacts to compare against
            out["synthesis"] = {"removed": self.removed(found, synth)}
        if verified is not None:
            # against synthesis where it ran, and against the find stage where it did not: a
            # --no-synthesis round still verifies, and requiring the middle snapshot reported it as
            # having done nothing at all
            out["verify"] = self.transition(synth if synth is not None else found, verified)
        return out

    @staticmethod
    def removed(before: dict[str, Any], after: dict[str, Any]) -> int:
        """Ids that reached a stage and left it, which for synthesis is merges and drops together —
        the artifacts do not distinguish them, and a merge is not attrition.
        """
        was = {f.get("id") for f in findings_of(before) if f.get("id")}
        now = {f.get("id") for f in findings_of(after) if f.get("id")}
        return len(was) - len(now)

    def transition(self, before: dict[str, Any], after: dict[str, Any]) -> dict[str, Any]:
        """What one stage did: what it removed, and which severities it moved in which direction.

        Only for a stage that judges a finding in place. A stage that merges files its output under one
        of its inputs' ids, so an id lookup here compares against an arbitrary member — see `measure`.
        """
        was = {f.get("id"): f for f in findings_of(before) if f.get("id")}
        now = {f.get("id"): f for f in findings_of(after) if f.get("id")}
        promoted, demoted = 0, 0
        demoted_lenses: list[str] = []
        for fid, new in now.items():
            old = was.get(fid)
            if old is None:
                continue
            # ranked rather than tested against minor at one end: critical -> major is a demotion the
            # verifier really makes, and comparing only to minor counted it as neither direction. The
            # same defect lived in app/pipeline/verify.go and was fixed there first
            was_rank, now_rank = rank(old.get("severity")), rank(new.get("severity"))
            if was_rank == now_rank:
                continue
            if now_rank > was_rank:
                promoted += 1
            else:
                demoted += 1
                # only a finding carrying one lens can be attributed. After synthesis a finding's lenses
                # is the union across everything merged into it, so crediting them all would charge a
                # lens that rated it minor with a demotion another lens earned
                lenses = new.get("lenses") or []
                if len(lenses) == 1:
                    demoted_lenses.append(str(lenses[0]))
        return {"removed": len(was) - len(now), "promoted": promoted, "demoted": demoted,
                "demoted_lenses": demoted_lenses}

    def by_lens(self, found: dict[str, Any]) -> dict[str, dict[str, int]]:
        """Severity as each lens raised it, before any stage touched it."""
        out: dict[str, dict[str, int]] = defaultdict(lambda: defaultdict(int))
        for f in findings_of(found):
            for lens in f.get("lenses") or []:
                out[lens][str(f.get("severity"))] += 1
        return {k: dict(v) for k, v in out.items()}

    def lens_naming(self, found: dict[str, Any]) -> dict[str, dict[str, int]]:
        """How many lenses each agent named per finding.

        `revmux stats` reports an `ambiguous` count that cannot tell a model deliberately naming both of
        its lenses from the fallback that assigns the whole set when it names none. This distinguishes
        them by agent: a fallback would fire at similar rates everywhere, so a wide spread between agents
        means the models are answering, and the ambiguity is real overlap rather than a measurement gap.
        """
        out: dict[str, dict[str, int]] = defaultdict(lambda: defaultdict(int))
        for f in findings_of(found):
            sources = f.get("sources") or ["?"]
            out[str(sources[0])][str(len(f.get("lenses") or []))] += 1
        return {k: dict(v) for k, v in out.items()}


def totals(rounds: list[dict[str, Any]]) -> dict[str, Any]:
    """Fold every round together."""
    out: dict[str, Any] = {
        "rounds": len(rounds),
        "raised": sum(r["raised"] for r in rounds),
        "gating": sum(r.get("gating", 0) for r in rounds),
        "minor": sum(r.get("minor", 0) for r in rounds),
        "by_lens": defaultdict(lambda: defaultdict(int)),
        "lens_naming": defaultdict(lambda: defaultdict(int)),
        "demoted_lenses": defaultdict(int),
    }
    # each stage's accumulator names what that stage measures, so a key nothing recorded cannot fold to
    # a zero that reads as a measurement. Synthesis has no severity movement to measure — see `measure`
    out["synthesis"] = {"removed": 0, "rounds": 0}
    out["verify"] = {"removed": 0, "promoted": 0, "demoted": 0, "rounds": 0}

    for r in rounds:
        for lens, sevs in r["by_lens"].items():
            for sev, n in sevs.items():
                out["by_lens"][lens][sev] += n
        for agent, counts in r["lens_naming"].items():
            for named, n in counts.items():
                out["lens_naming"][agent][named] += n
        for stage in ("synthesis", "verify"):
            moved = r.get(stage)
            if moved is None:
                continue
            out[stage]["rounds"] += 1
            for key in out[stage]:
                if key != "rounds":
                    out[stage][key] += moved[key]
            if stage == "verify":
                for lens in moved["demoted_lenses"]:
                    out["demoted_lenses"][lens] += 1

    out["by_lens"] = {k: dict(v) for k, v in out["by_lens"].items()}
    out["lens_naming"] = {k: dict(v) for k, v in out["lens_naming"].items()}
    out["demoted_lenses"] = dict(out["demoted_lenses"])
    return out


def conclusions(agg: dict[str, Any], rounds: list[dict[str, Any]]) -> list[str]:
    """The findings a suggestion is built from, each stated with the number behind it.

    Only what the corpus supports. A measurement that says nothing produces no line rather than a hedged
    one — an analysis that always finds something is one nobody can act on.
    """
    out: list[str] = []
    if agg["rounds"] < 3:
        out.append(f"Only {agg['rounds']} rounds recorded. Too thin to conclude anything; report the "
                   f"numbers and propose no change.")
        return out

    ver, syn = agg["verify"], agg["synthesis"]
    if ver["rounds"]:
        # what verification is depends on what it did. asserting one shape whatever the counts say is
        # the same defect as judging it on in/out alone, one level up
        moved = ver["demoted"] + ver["promoted"]
        if moved > ver["removed"]:
            out.append(
                f"Verification is a severity corrector rather than a filter here: across "
                f"{ver['rounds']} rounds it removed {ver['removed']} findings while lowering the "
                f"severity of {ver['demoted']} and raising {ver['promoted']}. Judge it on those moves — "
                f"without it they reach the user at the severity the finder gave them.")
        elif ver["removed"] > moved:
            out.append(
                f"Verification is filtering here rather than correcting: across {ver['rounds']} rounds "
                f"it removed {ver['removed']} findings and moved the severity of {moved}. Check what it "
                f"rejected before trusting the count — a filter this active is also where a real "
                f"finding disappears.")
        else:
            out.append(
                f"Verification removed {ver['removed']} findings and moved {moved} severities across "
                f"{ver['rounds']} rounds — too even to call it a filter or a corrector on this corpus.")
    if syn["rounds"] and not ver["rounds"]:
        # the comparison below needs both sides measured. A verifier that never ran leaves removed at
        # its zero initialisation, which is indistinguishable from one that removed nothing
        out.append(
            f"Synthesis removed {syn['removed']} findings across {syn['rounds']} rounds. Verification "
            f"never ran in this corpus, so there is nothing to compare it against — the `dropped` "
            f"events say which ones went, and a dropped critical is worth looking at.")
    elif syn["rounds"]:
        # per round on each side. Each stage is folded only over the rounds carrying its own snapshot,
        # so raw totals put different denominators either side of the comparison and invert it whenever
        # the stages ran over different numbers of rounds
        syn_rate, ver_rate = syn["removed"] / syn["rounds"], ver["removed"] / ver["rounds"]
        scope = (f"{syn['removed']} over {syn['rounds']} rounds against verification's "
                 f"{ver['removed']} over {ver['rounds']}")
        # named for what the counts say, like the verification sentence above it. asserting "the real
        # filter" whatever they were is the same defect, and it lived one line below the fix for it
        if syn_rate > ver_rate:
            out.append(
                f"Synthesis removes more per round than verification does: {scope}. That is merges and "
                f"drops together — the artifacts do not separate them — so read the `dropped` events "
                f"for what actually went, and treat a dropped critical as worth looking at.")
        elif ver_rate > syn_rate:
            out.append(
                f"Synthesis removed {scope}, so per round it is not where this corpus loses them. The "
                f"`dropped` events say which ones went.")
        else:
            out.append(
                f"Synthesis and verification remove at the same rate here: {scope}. Neither is where "
                f"this corpus loses findings — read the `dropped` events for what synthesis took.")

    inflaters = severity_inflaters(agg)
    if inflaters:
        lens, share, demotions = inflaters[0]
        # both sides of this ratio are single-lens demotions. verify's total includes merged findings
        # whose lens list cannot say which lens earned the severity, and dividing by that would compare
        # a restricted numerator with an unrestricted denominator
        attributable = sum(agg["demoted_lenses"].values())
        hardest = (f"`{lens}` rates hardest: {share}% of what it raises is major or critical. If "
                   f"severity feels inflated, that lens's text is the lever.")
        if ver["rounds"]:
            # the demotion half needs a verifier that ran. Without one every count in it is the zero it
            # was initialised to, and "0 of 0 demotions" reads as a lens nothing ever corrected
            hardest = (
                f"`{lens}` rates hardest: {share}% of what it raises is major or critical, and it "
                f"accounts for {demotions} of the {attributable} demotions that name a single lens "
                f"(verification made {ver['demoted']} in total; the rest name several and cannot be "
                f"attributed). If severity feels inflated, that lens's text is the lever.")
        out.append(hardest)

    total = agg["gating"] + agg["minor"]
    if total and agg["minor"] / total > 0.6:
        out.append(
            f"{round(100 * agg['minor'] / total)}% of everything reported is minor "
            f"({agg['minor']} of {total}). Minors do not converge the way gating findings do, so a "
            f"re-review under a profile that reports nothing below major is the shorter list.")

    # any round that did not improve on the one before it. Comparing each round with round 1 alone
    # misses 0 -> 0 -> 2 -> 1, which is the shape of a fix introducing a defect mid-sequence
    stalled = [t for t, seq in gating_paths(rounds).items()
               if len(seq) > 1 and any(b >= a for a, b in zip(seq, seq[1:]) if a > 0 or b > 0)]
    if stalled:
        out.append(
            f"{len(stalled)} task(s) had the gating count hold or rise from one round to the next "
            f"({', '.join(stalled)}). That is usually fixes introducing defects rather than the review "
            f"churning — compare what each round found before spending another one.")
    return out


def severity_inflaters(agg: dict[str, Any], min_raised: int = 10) -> list[tuple[str, int, int]]:
    """Lenses ranked by the share of their findings raised at major or above, worst first.

    The demotion column counts only demotions of findings carrying a single lens, since a merged
    finding's lens list cannot say which of them earned the severity.

    `min_raised` is the bar for naming a lens in a conclusion, where a share taken over three findings
    is noise. A table listing every lens passes `0`: dropping a row there reports a lens that raised
    nothing, and it is the newest lens — the one a tuning suggestion is most likely to be about — that
    sits under any cutoff longest.
    """
    out: list[tuple[str, int, int]] = []
    for lens, sevs in agg["by_lens"].items():
        raised = sum(sevs.values())
        if raised < min_raised:
            continue
        hard = sum(n for sev, n in sevs.items() if sev in GATING)
        out.append((lens, round(100 * hard / raised), agg["demoted_lenses"].get(lens, 0)))
    return sorted(out, key=lambda row: row[1], reverse=True)


def gating_paths(rounds: list[dict[str, Any]]) -> dict[str, list[int]]:
    """Each task's gating count in round order, which is how convergence is read."""
    out: dict[str, list[int]] = defaultdict(list)
    for r in rounds:
        if "gating" in r:
            out[r["task"]].append(r["gating"])
    return dict(out)


def resolve_tasks_dir(given: str | None) -> Path:
    """The tasks root, from the flag or from `revmux config` so the two can never disagree."""
    if given:
        return Path(given)
    try:
        out = subprocess.run(["revmux", "config"], capture_output=True, text=True, timeout=30, check=True)
        return Path(json.loads(out.stdout)["paths"]["tasks_dir"])
    except (OSError, subprocess.SubprocessError, ValueError, KeyError) as err:
        print(f"error: could not resolve the tasks root from `revmux config`: {err}", file=sys.stderr)
        print("       pass --tasks-dir explicitly", file=sys.stderr)
        raise SystemExit(1) from err


def render(agg: dict[str, Any], rounds: list[dict[str, Any]], unreadable: list[str]) -> None:
    """Print the conclusions, then the table each one rests on."""
    lines = conclusions(agg, rounds)
    print(f"{agg['rounds']} rounds over {len(gating_paths(rounds))} tasks, "
          f"{agg['raised']} findings raised.\n")
    if unreadable:
        print(f"{len(unreadable)} round(s) would not decode and are left out: "
              f"{', '.join(unreadable)}\n")
    for i, line in enumerate(lines, 1):
        print(f"{i}. {line}\n")

    print("severity as raised, by lens:")
    print(f"   {'lens':<14} {'major+':>7} {'minor':>7} {'% hard':>7} {'demoted':>8}")
    for lens, share, demotions in severity_inflaters(agg, min_raised=0):
        sevs = agg["by_lens"][lens]
        hard = sum(n for sev, n in sevs.items() if sev in GATING)
        print(f"   {lens:<14} {hard:>7} {sevs.get('minor', 0):>7} {share:>6}% {demotions:>8}")

    print("\ngating by round, per task:")
    for task, seq in gating_paths(rounds).items():
        print(f"   {task:<28} {' -> '.join(str(n) for n in seq)}")

    naming = agg["lens_naming"]
    # any multi-lens finding, not exactly two: a three-lens agent would otherwise suppress the table
    # that exists to show its ambiguity
    if any(int(k) > 1 and v for counts in naming.values() for k, v in counts.items()):
        print("\nlenses named per finding (a wide spread means the models are answering, so `ambiguous`")
        print("in `revmux stats` is counting real overlap rather than the fallback):")
        for agent, counts in sorted(naming.items()):
            named = ", ".join(f"{k} lens: {v}" for k, v in sorted(counts.items()))
            print(f"   {agent:<14} {named}")


def main() -> None:
    parser = argparse.ArgumentParser(description="report what a revmux archive says about the review")
    parser.add_argument("--tasks-dir", help="tasks root; default is what `revmux config` reports")
    parser.add_argument("--json", action="store_true", help="full measurements as JSON")
    parser.add_argument("--test", action="store_true", help="run unit tests")
    args = parser.parse_args()

    if args.test:
        run_tests()
        return

    tasks_dir = resolve_tasks_dir(args.tasks_dir)
    if not tasks_dir.is_dir():
        print(f"error: no tasks root at {tasks_dir}", file=sys.stderr)
        raise SystemExit(1)

    corpus = Corpus(tasks_dir)
    corpus.scan()
    if not corpus.rounds:
        print(f"no rounds have run under {tasks_dir} yet — nothing to analyze")
        return

    agg = totals(corpus.rounds)
    if args.json:
        print(json.dumps({"tasks_dir": str(tasks_dir), "totals": agg, "rounds": corpus.rounds,
                          "unreadable": corpus.unreadable, "conclusions": conclusions(agg, corpus.rounds)},
                         indent=2))
        return
    render(agg, corpus.rounds, corpus.unreadable)


def run_tests() -> None:
    import unittest

    class TestTransition(unittest.TestCase):
        def setUp(self) -> None:
            self.c = Corpus(Path("."))

        def test_demotion_is_counted_with_its_lens(self) -> None:
            before = {"findings": [{"id": "a-1", "severity": "major", "lenses": ["adversarial"]}]}
            after = {"findings": [{"id": "a-1", "severity": "minor", "lenses": ["adversarial"]}]}
            got = self.c.transition(before, after)
            self.assertEqual(1, got["demoted"])
            self.assertEqual(["adversarial"], got["demoted_lenses"])

        # after synthesis a finding's lenses is the union across everything merged into it, so charging
        # them all would credit a lens that rated it minor with a demotion another lens earned
        def test_a_merged_finding_demotion_is_counted_but_not_attributed(self) -> None:
            before = {"findings": [{"id": "a-1", "severity": "major", "lenses": ["adversarial", "docs"]}]}
            after = {"findings": [{"id": "a-1", "severity": "minor", "lenses": ["adversarial", "docs"]}]}
            got = self.c.transition(before, after)
            self.assertEqual(1, got["demoted"])
            self.assertEqual([], got["demoted_lenses"])

        def test_promotion_is_counted_separately(self) -> None:
            before = {"findings": [{"id": "a-1", "severity": "minor", "lenses": ["bugs"]}]}
            after = {"findings": [{"id": "a-1", "severity": "critical", "lenses": ["bugs"]}]}
            self.assertEqual(1, self.c.transition(before, after)["promoted"])

        # a move can skip minor entirely, and testing one end against minor counted it as no change at
        # all — every demotion figure this script has ever reported was short by these
        def test_a_move_that_skips_minor_still_has_a_direction(self) -> None:
            down = self.c.transition(
                {"findings": [{"id": "a-1", "severity": "critical", "lenses": ["bugs"]}]},
                {"findings": [{"id": "a-1", "severity": "major", "lenses": ["bugs"]}]})
            self.assertEqual(1, down["demoted"])
            self.assertEqual(["bugs"], down["demoted_lenses"])

            up = self.c.transition(
                {"findings": [{"id": "a-1", "severity": "major", "lenses": ["bugs"]}]},
                {"findings": [{"id": "a-1", "severity": "critical", "lenses": ["bugs"]}]})
            self.assertEqual(1, up["promoted"])

        # reclassification moves a finding between arrays without removing it, and reading `findings`
        # alone would report that as attrition — the mistake this whole script exists to stop repeating
        def test_reclassified_finding_is_not_removed(self) -> None:
            before = {"findings": [{"id": "a-1", "severity": "major", "lenses": []}]}
            after = {"immaterial": [{"id": "a-1", "severity": "major", "lenses": []}]}
            self.assertEqual(0, self.c.transition(before, after)["removed"])

        # a --no-synthesis round writes no middle snapshot but still verifies; requiring one reported
        # the stage as having done nothing
        def test_verification_is_measured_without_a_synthesis_snapshot(self) -> None:
            import tempfile
            with tempfile.TemporaryDirectory() as tmp:
                rnd = Path(tmp)
                (rnd / "stages").mkdir()
                (rnd / "manifest.json").write_text('{"run":"01"}', encoding="utf-8")
                (rnd / "stages" / "1-found.json").write_text(
                    json.dumps({"findings": [{"id": "a-1", "severity": "major", "lenses": ["bugs"]}]}),
                    encoding="utf-8")
                (rnd / "stages" / "3-verified.json").write_text(
                    json.dumps({"findings": [{"id": "a-1", "severity": "minor", "lenses": ["bugs"]}]}),
                    encoding="utf-8")

                got = Corpus(rnd.parent).measure("t", "01", rnd)
                assert got is not None
                self.assertIn("verify", got)
                self.assertEqual(1, got["verify"]["demoted"])

        def test_dropped_finding_is_removed(self) -> None:
            before = {"findings": [{"id": "a-1"}, {"id": "a-2"}]}
            after = {"findings": [{"id": "a-1"}]}
            self.assertEqual(1, self.c.transition(before, after)["removed"])

        # synthesis files a merge under merged_ids[0] and keeps the highest severity of everything it
        # merged, so an id lookup compares the max against an arbitrary member and reads it as a
        # promotion. Every promotion this script reported for the stage was that
        def test_synthesis_records_removal_only(self) -> None:
            import tempfile
            with tempfile.TemporaryDirectory() as tmp:
                rnd = Path(tmp)
                (rnd / "stages").mkdir()
                (rnd / "manifest.json").write_text('{"run":"01"}', encoding="utf-8")
                (rnd / "stages" / "1-found.json").write_text(
                    json.dumps({"findings": [{"id": "a-1", "severity": "minor", "lenses": ["docs"]},
                                             {"id": "b-1", "severity": "major", "lenses": ["bugs"]}]}),
                    encoding="utf-8")
                (rnd / "stages" / "2-synthesized.json").write_text(
                    json.dumps({"findings": [{"id": "a-1", "severity": "major",
                                              "lenses": ["docs", "bugs"]}]}),
                    encoding="utf-8")

                got = Corpus(rnd.parent).measure("t", "01", rnd)
                assert got is not None
                self.assertEqual({"removed": 1}, got["synthesis"])
                self.assertEqual({"removed": 1, "rounds": 1}, totals([got])["synthesis"])

    class TestConclusions(unittest.TestCase):
        def test_a_thin_corpus_proposes_nothing(self) -> None:
            agg = totals([{"task": "t", "run": "01", "raised": 1, "by_lens": {}, "lens_naming": {}}])
            got = conclusions(agg, [])
            self.assertEqual(1, len(got))
            self.assertIn("Too thin", got[0])

        def test_verification_is_judged_on_demotions_not_removals(self) -> None:
            rounds = [{"task": "t", "run": f"0{i}", "raised": 5, "gating": 1, "minor": 1,
                       "by_lens": {}, "lens_naming": {},
                       "verify": {"removed": 0, "promoted": 0, "demoted": 3, "demoted_lenses": []}}
                      for i in range(1, 5)]
            got = " ".join(conclusions(totals(rounds), rounds))
            self.assertIn("severity corrector", got)
            self.assertIn("lowering the severity of 12", got)

        # the numerator counts single-lens demotions, so the denominator must too — dividing by every
        # demotion would compare a restricted count with an unrestricted one
        # the sentence follows the counts now: asserting one shape whatever they say was the defect
        def test_a_verification_that_mostly_removes_is_called_a_filter(self) -> None:
            rounds = [{"task": "t", "run": f"0{i}", "raised": 5, "gating": 1, "minor": 1,
                       "by_lens": {}, "lens_naming": {},
                       "verify": {"removed": 9, "promoted": 0, "demoted": 1, "demoted_lenses": []}}
                      for i in range(1, 5)]
            got = " ".join(conclusions(totals(rounds), rounds))
            self.assertIn("filtering here rather than correcting", got)
            self.assertNotIn("severity corrector", got)

        def test_an_even_split_claims_neither_shape(self) -> None:
            rounds = [{"task": "t", "run": f"0{i}", "raised": 5, "gating": 1, "minor": 1,
                       "by_lens": {}, "lens_naming": {},
                       "verify": {"removed": 2, "promoted": 1, "demoted": 1, "demoted_lenses": []}}
                      for i in range(1, 5)]
            got = " ".join(conclusions(totals(rounds), rounds))
            self.assertIn("too even to call it", got)

        def test_the_ratio_uses_attributable_demotions_on_both_sides(self) -> None:
            rounds = [{"task": "t", "run": "01", "raised": 12, "gating": 1, "minor": 1,
                       "lens_naming": {},
                       "by_lens": {"adversarial": {"major": 9, "minor": 3}},
                       "verify": {"removed": 0, "promoted": 0, "demoted": 5,
                                  "demoted_lenses": ["adversarial", "adversarial"]}}] * 3
            got = " ".join(conclusions(totals(rounds), rounds))
            self.assertIn("6 of the 6 demotions that name a single lens", got)
            self.assertIn("15 in total", got)

        def test_the_hardest_rating_lens_is_named(self) -> None:
            rounds = [{"task": "t", "run": f"0{i}", "raised": 12, "gating": 1, "minor": 1,
                       "lens_naming": {},
                       "by_lens": {"adversarial": {"major": 9, "minor": 3},
                                   "docs": {"major": 1, "minor": 11}},
                       "verify": {"removed": 0, "promoted": 0, "demoted": 2,
                                  "demoted_lenses": ["adversarial", "adversarial"]}}
                      for i in range(1, 4)]
            got = " ".join(conclusions(totals(rounds), rounds))
            self.assertIn("`adversarial` rates hardest", got)
            self.assertIn("75%", got)

        # a corpus run entirely under --no-verify leaves every verify total at its zero initialisation,
        # which the comparison read as a verifier that removed nothing
        def test_an_unrun_verifier_is_not_compared_against(self) -> None:
            rounds = [{"task": "t", "run": f"0{i}", "raised": 5, "gating": 1, "minor": 1,
                       "by_lens": {}, "lens_naming": {}, "synthesis": {"removed": 19}}
                      for i in range(1, 5)]
            got = " ".join(conclusions(totals(rounds), rounds))
            self.assertIn("Verification never ran", got)
            self.assertNotIn("against verification's 0", got)

        # each stage folds only over the rounds carrying its own snapshot, so raw totals put different
        # denominators either side and invert the comparison
        def test_the_stage_comparison_is_per_round(self) -> None:
            rounds = [{"task": "t", "run": f"{i:02d}", "raised": 5, "gating": 1, "minor": 1,
                       "by_lens": {}, "lens_naming": {}, "synthesis": {"removed": 5}}
                      for i in range(1, 11)]
            for r in rounds[:2]:
                r["verify"] = {"removed": 20, "promoted": 0, "demoted": 0, "demoted_lenses": []}
            got = " ".join(conclusions(totals(rounds), rounds))
            # synthesis removes 5 a round against verification's 20, though its raw total is larger
            self.assertNotIn("Synthesis removes more per round", got)
            self.assertIn("50 over 10 rounds against verification's 40 over 2", got)

        # a tie fell into the lower-rate branch, printing "not where this corpus loses them" beside the
        # equal counts that contradict it. The verification sentence above already had the third branch
        def test_equal_removal_rates_claim_neither_stage(self) -> None:
            rounds = [{"task": "t", "run": f"0{i}", "raised": 5, "gating": 1, "minor": 1,
                       "by_lens": {}, "lens_naming": {}, "synthesis": {"removed": 2},
                       "verify": {"removed": 2, "promoted": 0, "demoted": 0, "demoted_lenses": []}}
                      for i in range(1, 5)]
            got = " ".join(conclusions(totals(rounds), rounds))
            self.assertIn("remove at the same rate here", got)
            self.assertNotIn("not where this corpus loses them", got)
            self.assertNotIn("removes more per round", got)

        # the demotion half of the sentence needs a verifier that ran; without one it read "0 of 0"
        def test_the_hardest_lens_omits_demotions_when_nothing_verified(self) -> None:
            rounds = [{"task": "t", "run": f"0{i}", "raised": 12, "gating": 1, "minor": 1,
                       "lens_naming": {}, "by_lens": {"adversarial": {"major": 9, "minor": 3}}}
                      for i in range(1, 4)]
            got = " ".join(conclusions(totals(rounds), rounds))
            self.assertIn("`adversarial` rates hardest", got)
            self.assertNotIn("demotions that name a single lens", got)

        def test_a_stalled_gating_count_is_called_out(self) -> None:
            rounds = [{"task": "stuck", "run": "01", "raised": 3, "gating": 3, "minor": 0,
                       "by_lens": {}, "lens_naming": {}},
                      {"task": "stuck", "run": "02", "raised": 3, "gating": 3, "minor": 0,
                       "by_lens": {}, "lens_naming": {}},
                      {"task": "stuck", "run": "03", "raised": 2, "gating": 2, "minor": 0,
                       "by_lens": {}, "lens_naming": {}}]
            self.assertIn("hold or rise", " ".join(conclusions(totals(rounds), rounds)))

        def test_a_mid_sequence_rise_is_caught(self) -> None:
            # comparing each round with round 1 alone misses this, and it is the shape that matters:
            # two clean rounds, then a fix that introduced something
            rounds = [{"task": "midrise", "run": f"0{i}", "raised": 3, "gating": g, "minor": 0,
                       "by_lens": {}, "lens_naming": {}}
                      for i, g in enumerate([0, 0, 2, 1], start=1)]
            self.assertIn("hold or rise", " ".join(conclusions(totals(rounds), rounds)))

        def test_a_converging_task_is_not_called_out(self) -> None:
            rounds = [{"task": "fine", "run": f"0{i}", "raised": 3, "gating": g, "minor": 0,
                       "by_lens": {}, "lens_naming": {}}
                      for i, g in enumerate([5, 2, 0], start=1)]
            self.assertNotIn("hold or rise", " ".join(conclusions(totals(rounds), rounds)))

    class TestSeverityInflaters(unittest.TestCase):
        # the cutoff exists so a conclusion does not name a lens on three findings. Letting it drive the
        # table too printed a lens that raised nothing, and a new lens sits under it longest
        def test_the_table_keeps_a_lens_the_ranking_drops(self) -> None:
            agg = {"by_lens": {"bugs": {"major": 30, "minor": 30},
                               "comments": {"major": 6, "minor": 3}},
                   "demoted_lenses": {}}
            self.assertEqual(["bugs"], [row[0] for row in severity_inflaters(agg)])
            self.assertEqual(["comments", "bugs"],
                             [row[0] for row in severity_inflaters(agg, min_raised=0)])

    class TestFindingsOf(unittest.TestCase):
        def test_all_four_arrays_are_counted(self) -> None:
            rep = {"findings": [{"id": "1"}], "open_questions": [{"id": "2"}],
                   "pre_existing": [{"id": "3"}], "immaterial": [{"id": "4"}]}
            self.assertEqual(4, len(findings_of(rep)))

        def test_absent_and_null_arrays_are_empty(self) -> None:
            self.assertEqual(0, len(findings_of({"findings": None})))

    # the cases are local to this function, so unittest.main would discover none of them
    loader = unittest.TestLoader()
    suite = unittest.TestSuite(loader.loadTestsFromTestCase(c)
                               for c in (TestTransition, TestConclusions, TestSeverityInflaters,
                                         TestFindingsOf))
    result = unittest.TextTestRunner(verbosity=2).run(suite)
    if not result.wasSuccessful():
        raise SystemExit(1)


if __name__ == "__main__":
    try:
        main()
    except KeyboardInterrupt:
        print("\ninterrupted", file=sys.stderr)
        raise SystemExit(130) from None
