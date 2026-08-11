# tracker/ — application tracking

## Tracker rules

`data/applications.md` is the source of truth. `tracker/tracker.mjs` maintains an
optional SQLite index derived from it (safe to delete, regenerates on sync).

1. **Never hand-add rows.** Write a TSV to `batch/tracker-additions/` and let
   `tracker/merge-tracker.mjs` merge it. Editing status/notes of *existing* rows is fine.
2. Never create a second row for a company+role that already exists.
3. Statuses must be canonical (`templates/states.yml`): `Evaluated`, `Applied`,
   `Responded`, `Interview`, `Offer`, `Rejected`, `Discarded`, `SKIP`. No bold,
   no dates, no extra prose in the status cell.
4. A literal `|` in any cell corrupts the row and every column after it — both
   parsers split on raw pipes (`tracker/merge-tracker.mjs`,
   `tracker/verify-pipeline.mjs`). `buildRow()` substitutes it with `/`.
5. Reports need `**URL:**` and `**Legitimacy:**` in the header.

TSV format — 9 tab-separated columns, **status before score** (merge swaps them
to match the tracker's score-before-status layout):

```
{num}\t{date}\t{company}\t{role}\t{status}\t{score}/5\t{Y|N}\t[{num}](reports/{num}-{slug}-{date}.md)\t{note}
```

The report link is always written root-relative; `tracker/merge-tracker.mjs` rewrites it
relative to the tracker's own directory (idempotent; `--migrate` fixes old rows).

Health: `node tracker/verify-pipeline.mjs` · normalize: `tracker/normalize-statuses.mjs` ·
dedup: `tracker/dedup-tracker.mjs`

