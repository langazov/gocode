All of these are valid:
- MERGE across rounds -- including a candidate REJECTED earlier, if a later rollout now argues for it.
- REPRODUCE an earlier checkpoint verbatim, if that state was simply the best one and later rounds drifted away from it.
- ADOPT this round's best candidate.
- KEEP_BASE.

{{keep_base}}

Here `KEEP_BASE` means keep the CURRENT checkpoint in `context/harness/` unchanged; it does not restore the original harness. In `<notes>`, identify which rounds' edits survive and which were dropped or restored.
