# Customization Guide

## Profile (config/profile.yml)

This is the single source of truth for your identity. All modes read from here.

Key sections:
- **candidate**: Name, email, phone, location, LinkedIn, portfolio
- **target_roles**: Your North Star roles and archetypes
- **narrative**: Your headline, exit story, superpowers, proof points
- **compensation**: Target range, minimum, currency
- **location**: Country, timezone, visa status, on-site availability

## Target Roles (config/profile.md)

The archetype table in `config/profile.md` determines how offers are scored and CVs are framed. Edit the table to match YOUR career targets:

```markdown
| Archetype | Thematic axes | What they buy |
|-----------|---------------|---------------|
| **Your Role 1** | key skills | what they need |
| **Your Role 2** | key skills | what they need |
```

Also update the "Adaptive Framing" table to map YOUR specific projects to each archetype.

## Portals (portals.yml)

Copy from `templates/portals.example.yml` and customize:

1. **title_filter.positive**: Keywords matching your target roles
2. **title_filter.negative**: Tech stacks or domains to exclude
3. **search_queries**: WebSearch queries for job boards (Ashby, Greenhouse, Lever)
4. **tracked_companies**: Companies to check directly

## CV Template (templates/cv-template.tex)

[Jake's Resume](https://github.com/jakegut/resume), compiled by `pdflatex`:
- **Font**: Latin Modern (the LaTeX default) -- no font files to manage
- **Layout**: Single-column, A4, 0.45in margins, ruled small-caps headings
- **Sections**: Technical Skills, Work Experience, Projects, Education

To customize spacing or section order, edit `templates/cv-template.tex` directly.
Requires TeX Live (`titlesec`, `enumitem`, `tabularx`, `marvosym`, `fancyhdr`,
`glyphtounicode`). Colours are deliberate: there are none, because the palette
was the least ATS-safe thing in the old template.


## Negotiation Scripts (modes/_shared.md)

The negotiation section provides frameworks for salary discussions. Replace the example scripts with your own:
- Target ranges
- Geographic arbitrage strategy
- Pushback responses

## Hooks (Optional)

Snipe can integrate with external systems via Claude Code hooks. Example hooks:

```json
{
  "hooks": {
    "SessionStart": [{
      "hooks": [{
        "type": "command",
        "command": "echo 'Snipe session started'"
      }]
    }]
  }
}
```

Save hooks in `.claude/settings.json` (Claude Code). OpenCode does not support hooks. For equivalent functionality, use custom commands (`.opencode/commands/`) or agents (`.opencode/agents/`) — see https://opencode.ai/docs/commands/.

## States (templates/states.yml)

The canonical states rarely need changing. If you add new states, update:
1. `templates/states.yml`
2. `internal/tracker` (alias mappings, applied by `snipe tracker fix`)
3. `modes/_shared.md` (any references)
