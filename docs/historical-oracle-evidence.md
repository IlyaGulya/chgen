# Historical oracle evidence

The files in this index are immutable measurement records or design records.
They can contain old grammar names, old fixture names, old report shapes, and
old commands. Do not use those names or commands for a current oracle run.

The current contract has one sampling profile, `current-combined-v1`, and one
fixture, `current-fixture`. Select the profile only with
`CHGEN_ORACLE_PLAN=current-combined-v1`. The retired
`CHGEN_ORACLE_GRAMMAR` interface must cause an explicit refusal.

## Archived records

| File | Historical content |
| --- | --- |
| [binary-operator-fallback-survey.md](binary-operator-fallback-survey.md) | Historical operator fallback survey |

The retained survey is archive-only. A missing current identity cannot be
reconstructed from an old artifact. Do not add identity fields by hand.

The legacy converter accepts only exact allowlisted source bytes. It can copy
retirement audit into the current baseline in one direction. It does not copy
accepted signatures, and it does not turn an old report into a current report.
