# Decision: Bool against UInt8 (reversed)

Status: superseded. The equivalence this document once described was
REMOVED. This document now records what happened and why, so a later
author does not restore it by accident.

## The earlier decision (the regression), and why it was wrong

ClickHouse has no separate boolean storage type. `Bool` is an alias of
`UInt8`, and `toTypeName()` reports the alias that the context selects.
The type oracle once found that for many predicates the server reports
`UInt8` while chgen inferred `Bool`, and it recorded the family as a
`MISMATCH`.

the regression answered this by adding a normaliser: the oracle treated a
`Bool`-against-`UInt8` pair as an accepted divergence, class
`OK_ACCEPTED_DIVERGENCE`, instead of `MISMATCH`. The report carried the
count in an uncapped map, `accepted_divergence_signatures`, so the class
could be audited.

That rewrite was never measured against the server. It was applied
sight unseen, and it silenced 571 wrapper-grid cells at once. Hiding
that many cells behind one equivalence hid a real defect: the divergence
was not "Bool is a display alias of UInt8 everywhere," it was "chgen
invents Bool for a whole family of predicate results that ClickHouse
answers as plain UInt8, and separately, chgen was ALSO wrong in a
different way for `groupBitAnd`/`Or`/`Xor`, which the equivalence
papered over instead of catching." See the regression.

## What is true today

There is no Bool-against-UInt8 equivalence in the oracle any more, and
`OK_ACCEPTED_DIVERGENCE` no longer exists as a class. The predicate
family (`=`, `<`, `<=`, `>`, `>=`, `isNull`, `isNotNull`, `empty`,
`notEmpty`, `has`, `arrayExists`, `arrayAll`, and the function forms
`equals`/`less`) now answers `UInt8` in chgen's own inference, for the
SAME reason the server does: chgen no longer invents `Bool` for a
predicate result. The two sides agree as TEXT, and the oracle needs no
equivalence there. See `TestPredicateFamilyAnswersUInt8EvenOverBool` in
`typeoracle_fuzz_test.go`.

The `empty(s)` canary in `canaryExprs` used to be the
Bool-against-UInt8 blindness probe: it fired an accepted-divergence
finding, and its firing proved the oracle could still see the
divergence. Now the predicate rule makes chgen and the server agree
outright, so `empty(s)` reports no finding at all, exactly like the
other closed canaries (`1`, `-1`, `s LIKE '%a%'`). See the canary check
in `TestTypeOracle` (search for `assertNoFinding("empty(s)")`).

Bool is still preserved, but only on the OTHER side of the family: `AND`,
`OR` and `NOT` are operations ON Bool, not predicates, and they keep the
`Bool` base under a single measured rule (at least one operand carries a
bare `Bool`, outside a `Nullable` wrapper and outside a surviving
`SimpleAggregateFunction` marker). See `TestLogicOperatorsPreserveBool`.
This rule replaced an earlier, wrong counting rule that flipped the base
to `UInt8` at a count of two `Nullable` or `SimpleAggregateFunction`
operands regardless of a bare `Bool` operand elsewhere in the
expression; that counting rule was a silently wrong type on cells the
wrapper grid did not cover. The regression fixed the widening rule to the
single predicate now in place.

## Why the field is gone, not just unused

`AcceptedDivergenceSig` (`accepted_divergence_signatures` in the JSON)
had no writer once the equivalence was removed: the map was always
empty, forever. A count that can never move is a check that can never
fail. It was removed from `Report`, `Volume` and `Compare` in
`internal/oraclereport/report.go`, together with the printed volume line
and the diff. Nothing outside `internal/oraclereport` read the JSON key.

## Rules for a later author

- Do not reintroduce a Bool-against-UInt8 normaliser in the oracle. If a
  future measurement finds a NEW divergence that looks similar, treat it
  as its own question: measure it against a live server first, write a
  new decision document, and give it a class that a report field can
  actually move for. Do not resurrect `OK_ACCEPTED_DIVERGENCE` by name.
- If chgen's inference for the predicate family ever needs to answer
  `Bool` again, that is a change to `TestPredicateFamilyAnswersUInt8EvenOverBool`
  and to the inference code together, not a change to the oracle's
  classifier. Do not paper over a real disagreement with a report-side
  equivalence a second time.
- Measure a claim about the driver or the server against a live
  instance. The claims above were measured with ClickHouse 25.8.29.51.

## Evidence

- the regression: added the Bool-against-UInt8 equivalence and the
  `OK_ACCEPTED_DIVERGENCE` class, unmeasured.
- the regression: found and fixed the actual defect the equivalence had
  hidden (the `AND`/`OR`/`NOT` widening rule, and the
  `groupBitAnd`/`Or`/`Xor` case), and removed the equivalence. See the
  comment above `TestPredicateFamilyAnswersUInt8EvenOverBool` in
  `typeoracle_fuzz_test.go`.
- This update removed the dead `AcceptedDivergenceSig`
  field, which the regression's reversal left behind with no writer, and
  corrected this document, which the regression's reversal left pointing at
  the OLD behaviour.
