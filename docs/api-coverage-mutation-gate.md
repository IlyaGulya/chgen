# API coverage mutation gate

The API coverage gate proves that each coverage signal can fail. A positive
count alone is not evidence because an attempt can fail before it measures a
cell.

The default test suite applies these mutations:

- Remove an API inventory class.
- Remove an accepted type specimen from the oracle fixture.
- Remove a legal function probe, an illegal function probe, or an illegal
  argument position.
- Change the sampling profile hash.
- Record attempts with zero accepted sampling cells.
- Remove the execution witness from a conformance cell.
- Remove the execution-oracle distortion witness or the unsupported-type
  refusal witness.
- Mark an unsupported API item as measured support without its classification
  source.

Each mutation must make its owning validator refuse. The shared conformance
runner and both oracle gates also refuse a report with zero measured cells.

Run the gate with the default verification command:

```bash
./scripts/verify.sh
```

The live oracles remain separate. They prove that the independent ClickHouse
analysis, execution, HTTP, and native channels can disagree on real data.
