# Frontend finish checklist
- Before `ticket_finish` on a change touching `slop-haus/frontend`, run the two checks that most often turn the landing queue red, both under a minute:
  - `node dev-tools/style-check/stylelint_ratchet/ratchet.mjs slop-haus/frontend`
  - `cd slop-haus/frontend && node tools/import_boundaries.mjs`
- `slop-haus/frontend/test.sh` runs both. The landing queue hands a change back at the first failed check, so a failure found there costs a queue turn.
