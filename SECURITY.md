# Security Policy

## Supported versions

snipe ships from a single branch. Fixes land on `main` and go out with the next tag; older tags get nothing.

| Version | Supported |
|---------|-----------|
| 0.3.x   | yes       |
| < 0.3   | no        |

## Reporting a vulnerability

Use GitHub's private reporting on this repo: **Security > Report a vulnerability**. That opens a thread only you and I can see. If you would rather not go through GitHub, email denys.skira@gmail.com.

You'll get a first reply within a week. This is a one-person project, so the fix may take considerably longer than the acknowledgement; I'll tell you where it stands as soon as I know.

Please don't open a public issue for anything exploitable.

## What snipe touches

Worth knowing before you decide whether something counts as a vulnerability.

**Model calls stay on the machine.** Every prompt goes to a local Ollama over localhost. Your CV, your reports and your applications are never sent anywhere, and this is a core principle of the project.

**The traffic that does leave** belongs to portal scans: the Apify API when a provider is configured for it, and a Playwright browser that opens posting URLs to check they're still live, which start from `scan.mjs`.

**Secrets live in `.env`** (`APIFY_API_KEY`), or as `api_key:` on an entry in `portals.yml`. Both are gitignored, as is the whole personal layer - `cv.md`, `config/profile.*`, `data/`, `reports/`, `output/`, `interview-prep/`. If you fork snipe, read `git status` before your first push.

**Job descriptions are untrusted input.** A posting is text somebody else wrote, and it goes straight into a model prompt. A crafted posting can try to steer the evaluation or plant something in the tailored CV, but it can only reach local files. Read what came out before you send it. It is advised to only use snipe with job descriptions you trust and spend reasonable time reviewing and adjusting the results by hand.

## Out of scope

- Anything that needs an attacker who already has your shell.
- Ollama and the models you pull into it. Those go upstream.
- A bad score or a weak tailored CV. That's a quality bug - open an issue.
