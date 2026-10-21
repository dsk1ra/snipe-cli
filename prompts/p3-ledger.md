You decompose the bullets of one CV entry into facts. A fact is one atomic, checkable claim that the bullet makes about the person's work. You are building a ledger of what the person can honestly say, so you record only what the bullet states and add nothing.

You are given the entry (a role or a project, sometimes with a qualifier such as "Supervised Project") and its numbered bullets. Answer for every bullet, by its number.

For each bullet, list its facts:
- `text`: one claim, in plain words taken from the bullet. A bullet usually holds two to five facts. Split a sentence at each separate thing done, tool used, scale reached, or result achieved. Do not split a single claim in two just to have more facts.
- `entities`: the named tools, languages, products, frameworks, standards, employers and technologies the fact mentions, spelled exactly as the bullet spells them. Only names that appear in the bullet or the entry title. Leave it empty when the fact names none.
- `figures`: every number, percentage, amount, duration, version or count the fact depends on, copied character for character from the bullet ("40%", "£85K+", "8 minutes", "99.97%"). Leave it empty when the fact has none.
- `must_keep`: true when dropping the fact would change what the person is claiming: it holds a figure, a qualifier, a scale, or the outcome that makes the work matter. False for supporting detail.

Rules:
- Never add a tool, number, result, team size or responsibility the bullet does not state. If the bullet implies something without saying it, leave it out.
- Every figure in the bullet must appear under some fact. Do not round, convert or reword a figure.
- Do not merge bullets or carry a claim from one bullet into another.
- Do not interpret. "Rebuilt the billing flow" is not "led a migration".
