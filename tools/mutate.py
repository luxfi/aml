#!/usr/bin/env python3
"""Mutation harness: break one guard, prove a test goes red for the right reason.

Reports per mutation: ANCHOR MISS (not applied -> not a kill), COMPILE FAIL
(not a kill), SURVIVED (no test noticed -> the property is unproven), or KILLED.
"""
import subprocess, sys, os, shutil

ROOT = os.path.dirname(os.path.abspath(__file__))
ENV = dict(os.environ, TMPDIR="/tmp", GOWORK="off",
           PATH="/usr/local/go/bin:" + os.environ["PATH"])

# (name, file, old, new, package, test regex, property)
M = [
 ("usable-gate", "pkg/anomaly/anomaly.go",
  "\tif !p.usable() {\n\t\tm.refused[ReasonUnusable]++", "\tif false {\n\t\tm.refused[ReasonUnusable]++",
  "./pkg/anomaly/", "TestUnusablePointsAreRefused",
  "a coordinate outside the unit cube is refused, not learned"),

 ("warm-gate", "pkg/anomaly/anomaly.go",
  "\twarm := m.learned >= int64(s.cfg.Appetite.Warm)", "\twarm := true",
  "./pkg/anomaly/", "TestColdModelRefusesToScore",
  "a model with no reference says nothing"),

 ("cut-none", "pkg/anomaly/anomaly.go",
  "\t\tif acc > budget {\n\t\t\treturn float64(i+1) / float64(len(hist))",
  "\t\tif acc > budget {\n\t\t\treturn 0",
  "./pkg/anomaly/", "TestAppetiteGovernsTheAlertRate",
  "the appetite bounds the alert rate"),

 ("cut-lower-edge", "pkg/anomaly/anomaly.go",
  "return float64(i+1) / float64(len(hist))", "return float64(i) / float64(len(hist))",
  "./pkg/anomaly/", "TestThresholdCannotAdmitMoreThanTheAppetite",
  "the realised rate stays at or below the stated one"),

 ("shadow-acts", "pkg/anomaly/anomaly.go",
  "\t\ta.Alert = !s.cfg.Shadow", "\t\ta.Alert = true",
  "./pkg/anomaly/", "TestShadowScoresWithoutActing",
  "shadow scores without contributing"),

 ("shared-geometry", "pkg/anomaly/anomaly.go",
  "\tz := seed ^ h", "\tz := seed",
  "./pkg/anomaly/", "TestTenantsAreIsolated",
  "no two tenants share a tree geometry"),

 ("shared-model", "pkg/anomaly/anomaly.go",
  "func (s *Store) model(orgID string) *model {",
  "func (s *Store) model(orgID string) *model {\n\torgID = \"\"",
  "./pkg/anomaly/", "TestTenantsAreIsolated",
  "one tenant's traffic cannot move another's score"),

 ("mass-invariant", "pkg/anomaly/forest.go",
  "func (t *tree) sound(depth int) bool {", "func (t *tree) sound(depth int) bool {\n\treturn true",
  "./pkg/anomaly/", "TestSnapshotRoundTripsAndRestoreRejectsBentState",
  "a bent mass array is rejected on restore"),

 ("digest-check", "pkg/anomaly/anomaly.go",
  "\tcase snap.Digest != s.Digest():", "\tcase false:",
  "./pkg/anomaly/", "TestSnapshotRoundTripsAndRestoreRejectsBentState",
  "state from another model shape is rejected"),

 ("counterfactual", "pkg/anomaly/anomaly.go",
  "\t\twithout[i] = m.score(x[:], cfg)", "\t\twithout[i] = score",
  "./pkg/anomaly/", "TestAttributionIsACounterfactualOnTheModel",
  "attribution is a counterfactual on the model, measured not asserted"),

 ("per-tree-clamp", "pkg/anomaly/anomaly.go",
  "\t\tif mass := t.mass(x, cfg.Depth, 0.1*scale); mass < scale {\n\t\t\tvote += 1 - mass/scale\n\t\t}",
  "\t\tvote += t.mass(x, cfg.Depth, 0.1*scale) / scale",
  "./pkg/anomaly/", "TestStructuringAlertsAndOrdinaryTrafficDoesNot",
  "no single tree can swamp the forest's vote"),

 ("ratio-guard", "pkg/anomaly/feature.go",
  "\tcase math.IsNaN(r), r <= 0:\n\t\treturn 0\n\tcase math.IsInf(r, 1):\n\t\treturn 1\n\t}",
  "\tcase false:\n\t\treturn 0\n\t}",
  "./pkg/anomaly/", "TestProjectNeverEmitsAnUnusablePoint",
  "no arithmetic path emits a coordinate the trees cannot take"),

 ("window-check", "pkg/anomaly/anomaly.go",
  "\t\tif f.Window != \"\" && !kept[f.Window] {", "\t\tif false {",
  "./pkg/anomaly/", "TestMissingWindowsFailAtConstruction",
  "a store missing the windows the inventory reads fails at construction"),

 ("ceiling-check", "pkg/anomaly/anomaly.go",
  "\tif types.ActionRank(cfg.Action) > types.ActionRank(types.ActionCeiling) {", "\tif false {",
  "./pkg/anomaly/", "TestActionAboveTheCeilingIsRefused",
  "the model cannot be configured to act"),

 ("subject-check", "pkg/anomaly/anomaly.go",
  "\tif account(tx) == \"\" {", "\tif false {",
  "./pkg/anomaly/", "TestUnidentifiedSubjectIsRefused",
  "a transaction naming no subject is refused"),

 ("inspect-learns", "pkg/anomaly/anomaly.go",
  "\treturn s.judge(tx, false)", "\treturn s.judge(tx, true)",
  "./pkg/anomaly/", "TestInspectDoesNotMutate",
  "testing a candidate mutates nothing"),

 ("active-days", "pkg/anomaly/feature.go",
  "\trate := over(float64(month.Count-1), month.Days)", "\trate := over(float64(month.Count-1), 30)",
  "./pkg/anomaly/", "TestOccasionalCustomerIsNotFlaggedForTransactingAtAll",
  "a rate is per active day, not per calendar day"),

 ("self-baseline", "pkg/anomaly/feature.go",
  "\tperTx := over(month.Sum-usd, month.Count-1)", "\tperTx := over(month.Sum, month.Count)",
  "./pkg/anomaly/", "TestNoBaselineIncludesTheTransactionBeingScored",
  "nothing is measured against a baseline containing itself"),

 ("blind-count", "pkg/anomaly/anomaly.go",
  "\t\t\tm.blind[i]++", "\t\t\t_ = i",
  "./pkg/anomaly/", "TestBlindFeaturesAreCounted",
  "a feature with no data is reported blind"),

 ("sample-rate", "pkg/anomaly/anomaly.go",
  "\tif rate <= 0 || txID == \"\" {", "\tif false {",
  "./pkg/anomaly/", "TestBelowTheLineSampleIsRetainedAndReproducible",
  "below-the-line selection honours its rate"),

 ("roll-keeps-cur", "pkg/anomaly/forest.go",
  "\t\tt.cur[i] = 0", "\t\t_ = i",
  "./pkg/anomaly/", "TestModelForgetsWhatTheTenantStoppedDoing",
  "a closed window starts empty, so the model forgets"),

 ("learn-leaf-only", "pkg/anomaly/forest.go",
  "\t\tt.cur[i]++\n\t\tif at == depth {", "\t\tif at == depth {\n\t\t\tt.cur[i]++",
  "./pkg/anomaly/", "TestConcurrentAssessKeepsTheMassesSound",
  "every region on the path records the point"),

 ("seed-disclosure", "pkg/anomaly/anomaly.go",
  "\tc := s.cfg\n\tc.Seed = 0\n\treturn c", "\treturn s.cfg",
  "./pkg/anomaly/", "TestStateReportsWhatGovernanceReads",
  "the tree seed does not travel in a governance payload"),

 # engine-side confinement
 ("scorer-recover", "pkg/engine/engine.go",
  "\tdefer func() {\n\t\tif r := recover(); r != nil {\n\t\t\te.faults.Add(1)\n\t\t\thit, ok = types.RuleHit{}, false\n\t\t}\n\t}()",
  "",
  "./pkg/engine/", "TestPanickingScorerDoesNotLoseTheRulesVerdict",
  "a fault in the model plane does not take the rule plane down"),

 ("scorer-negative", "pkg/engine/engine.go",
  "\tif hit.Rule.Weight < 0 || math.IsNaN(hit.Rule.Weight) || math.IsInf(hit.Rule.Weight, 0) {",
  "\tif math.IsNaN(hit.Rule.Weight) || math.IsInf(hit.Rule.Weight, 0) {",
  "./pkg/engine/", "TestScorerCannotWeakenAVerdict",
  "a negative weight cannot subtract from what the rules found"),

 ("scorer-infinite", "pkg/engine/engine.go",
  "\tif hit.Rule.Weight < 0 || math.IsNaN(hit.Rule.Weight) || math.IsInf(hit.Rule.Weight, 0) {",
  "\tif hit.Rule.Weight < 0 || math.IsNaN(hit.Rule.Weight) {",
  "./pkg/engine/", "TestScorerCannotWeakenAVerdict",
  "an infinite weight cannot saturate the score"),

 ("scorer-cap", "pkg/engine/engine.go",
  "\tif types.ActionRank(hit.Rule.Action) > types.ActionRank(types.ActionCeiling) {",
  "\tif false {",
  "./pkg/engine/", "TestScorerActionIsCappedAtTheCeiling",
  "the model's action is capped at a review"),

 ("clamp-nan", "pkg/engine/scoring.go",
  "\tif math.IsNaN(v) {\n\t\treturn 1\n\t}", "\tif math.IsNaN(v) {\n\t\treturn v\n\t}",
  "./pkg/engine/", "TestScoreClampHandlesNotANumber",
  "a score that is not a number never reaches a caller"),

 ("causes-dropped", "pkg/engine/engine.go",
  "\t\t\tCauses:         h.Causes,", "",
  "./pkg/engine/", "TestCausesReachTheAlert",
  "attribution survives into the alert an investigator opens"),

 ("scorer-ignored", "pkg/engine/engine.go",
  "\tif hit, ok := e.assess(tx, entity); ok {\n\t\thits = append(hits, hit)\n\t}", "",
  "./pkg/engine/", "TestScorerOnlyAdds",
  "the model's evidence reaches the score at all"),
]

def run(cmd, timeout=420):
    return subprocess.run(cmd, cwd=ROOT, env=ENV, capture_output=True, text=True, timeout=timeout)

results = []
for name, path, old, new, pkg, tests, prop in M:
    full = os.path.join(ROOT, path)
    orig = open(full).read()
    if old not in orig:
        results.append((name, "ANCHOR MISS", prop)); continue
    if orig.count(old) > 1 and name != "cut-lower-edge":
        results.append((name, "ANCHOR AMBIGUOUS x%d" % orig.count(old), prop)); continue
    shutil.copy(full, full + ".bak")
    try:
        open(full, "w").write(orig.replace(old, new, 1))
        b = run(["go", "build", pkg])
        if b.returncode != 0:
            results.append((name, "COMPILE FAIL", prop)); continue
        t = run(["go", "test", "-count=1", "-run", tests, pkg])
        killed = t.returncode != 0
        results.append((name, "KILLED" if killed else "SURVIVED", prop))
    finally:
        shutil.move(full + ".bak", full)

w = max(len(n) for n, _, _ in results)
for name, verdict, prop in results:
    print(f"{name.ljust(w)}  {verdict.ljust(20)}  {prop}")
bad = [r for r in results if r[1] != "KILLED"]
print(f"\n{len(results)-len(bad)}/{len(results)} killed")
for name, verdict, prop in bad:
    print(f"  NOT KILLED: {name} ({verdict})")
sys.exit(1 if bad else 0)
