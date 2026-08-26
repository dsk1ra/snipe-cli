"""Compare a Phase 2 run between the Node implementation and this one.

    compare-phase2.py <work-dir> <id> [id...]

Exits non-zero if any offer differs. Two things it has to get right, both of
which it got wrong first:

  * Match the two reports by the filename each side recorded. Matching by
    position silently diffs one offer's report against another's.
  * Normalise the report *filename*, not the bare report number. The number is
    allocated rather than judged — the Node side is told one, this side picks
    the next free — but replacing the bare digits also hits URLs, "Java 21" and
    table row numbers, which makes identical reports look different.
"""

import difflib
import json
import os
import sys

# Volatile or run-scoped. Everything else, prose included, must match.
SKIP = ("report_path", "report_filename", "report_num", "id", "url", "status", "evaluator")


def norm_eval(path):
    d = json.load(open(path))
    for k in SKIP:
        d.pop(k, None)
    return json.dumps(d, indent=2, sort_keys=True, ensure_ascii=False)


def show(label, a, b, limit=30):
    print(label)
    for line in list(difflib.unified_diff(a.split("\n"), b.split("\n"), "js", "go", lineterm=""))[:limit]:
        print("  " + line[:200])


def main(work, ids):
    ok = bad = 0
    for offer in ids:
        je = f"{work}/js/batch/evals/{offer}.json"
        ge = f"{work}/go/batch/evals/{offer}.json"
        if not (os.path.exists(je) and os.path.exists(ge)):
            missing = je if not os.path.exists(je) else ge
            print(f"#{offer}: missing {missing}")
            bad += 1
            continue

        same = True
        a, b = norm_eval(je), norm_eval(ge)
        if a != b:
            same = False
            show(f"#{offer} eval DIFFERS", a, b)

        jr = json.load(open(je))["report_filename"]
        gr = json.load(open(ge))["report_filename"]
        jp, gp = f"{work}/js/reports/{jr}", f"{work}/go/reports/{gr}"
        if os.path.exists(jp) and os.path.exists(gp):
            x = open(jp).read().replace(jr, "REPORT")
            y = open(gp).read().replace(gr, "REPORT")
            if x != y:
                same = False
                show(f"#{offer} report DIFFERS", x, y)
        else:
            print(f"#{offer}: a report file is missing")
            same = False

        if same:
            ok += 1
            print(f"#{offer} identical")
        else:
            bad += 1

    print()
    print(f"phase2: {ok}/{ok + bad} identical" + (" — GREEN" if not bad else f" — {bad} DIFFER"))
    return 1 if bad else 0


if __name__ == "__main__":
    if len(sys.argv) < 3:
        sys.exit(__doc__)
    sys.exit(main(sys.argv[1], sys.argv[2:]))
