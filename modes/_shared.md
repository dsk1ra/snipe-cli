# System Context -- snipe

## Sources of Truth

| File | Path | When |
|------|------|------|
| cv.md | `cv.md` (project root) | ALWAYS |
| article-digest.md | `article-digest.md` (if exists) | ALWAYS (detailed proof points) |
| profile.yml | `config/profile.yml` | ALWAYS (candidate identity and targets) |
| config/profile.md | `config/profile.md` | ALWAYS (user archetypes, narrative, negotiation) |

**RULE: NEVER hardcode metrics from proof points.** Read them from cv.md + article-digest.md at evaluation time.
**RULE: For article/project metrics, article-digest.md takes precedence over cv.md.**
**RULE: Read config/profile.md AFTER this file. User customizations in config/profile.md override defaults here.**

---

## Scoring System

The evaluation uses 6 blocks (A-F) with a global score of 1-5:

| Dimension | What it measures |
|-----------|-----------------|
| Match con CV | Skills, experience, proof points alignment |
| North Star alignment | How well the role fits the user's target archetypes (from config/profile.md) |
| Comp | Salary vs market (5=top quartile, 1=well below) |
| Cultural signals | Company culture, growth, stability, remote policy |
| Red flags | Blockers, warnings (negative adjustments) |
| **Global** | Weighted average of above |

**Score interpretation:**
- 4.5+ → Strong match, recommend applying immediately
- 4.0-4.4 → Good match, worth applying
- 3.5-3.9 → Decent but not ideal, apply only if specific reason
- Below 3.5 → Recommend against applying

## Posting Legitimacy (Block G)

Block G assesses whether a posting is likely a real, active opening. It does NOT affect the 1-5 global score -- it is a separate qualitative assessment.

**Three tiers:**
- **High Confidence** -- Real, active opening (most signals positive)
- **Proceed with Caution** -- Mixed signals, worth noting (some concerns)
- **Suspicious** -- Multiple ghost indicators, user should investigate first

**Key signals (weighted by reliability):**

| Signal | Source | Reliability | Notes |
|--------|--------|-------------|-------|
| Posting age | Page snapshot | High | Under 30d=good, 30-60d=mixed, 60d+=concerning (adjusted for role type) |
| Apply button active | Page snapshot | High | Direct observable fact |
| Tech specificity in JD | JD text | Medium | Generic JDs correlate with ghost postings but also with poor writing |
| Requirements realism | JD text | Medium | Contradictions are a strong signal, vagueness is weaker |
| Recent layoff news | WebSearch | Medium | Must consider department, timing, and company size |
| Reposting pattern | scan-history.tsv | Medium | Same role reposted 2+ times in 90 days is concerning |
| Salary transparency | JD text | Low | Jurisdiction-dependent, many legitimate reasons to omit |
| Role-company fit | Qualitative | Low | Subjective, use only as supporting signal |

**Ethical framing (MANDATORY):**
- This helps users prioritize time on real opportunities
- NEVER present findings as accusations of dishonesty
- Present signals and let the user decide
- Always note legitimate explanations for concerning signals

## Archetype Detection

Classify every offer into the closest software-engineering archetype (or a hybrid of 2). This is a general SWE taxonomy — the candidate's *actual* target archetypes and adaptive framing live in `config/profile.md` and take precedence; use this table only to read the JD's signal.

| Archetype | Key signals in JD |
|-----------|-------------------|
| Backend / Distributed Systems | "APIs", "microservices", "event-driven", "databases", "scalability", "latency", "throughput" |
| Frontend / Web | "React", "TypeScript", "UI", "component", "accessibility", "browser", "design system" |
| Full-Stack | "end-to-end", "React + Node", "full stack", "product engineer", "ship features" |
| Platform / DevOps / SRE | "Kubernetes", "CI/CD", "observability", "reliability", "infrastructure", "on-call", "Terraform" |
| Data / ML / AI | "pipelines", "ML", "LLM", "RAG", "agents", "data engineering", "model", "evals" |
| Mobile | "iOS", "Android", "Swift", "Kotlin", "React Native", "mobile" |
| Security / AppSec | "OAuth/OIDC", "OWASP", "threat model", "pen-test", "zero trust", "vulnerabilities" |
| Embedded / Systems | "C/C++", "Rust", "firmware", "real-time", "drivers", "low-level", "kernel" |

After detecting the archetype, read `config/profile.md` for the candidate's specific framing and proof points for that archetype. If the offer fits none of the candidate's target archetypes, mark it "Outside targets".

## Global Rules

### NEVER

1. Invent experience or metrics
2. Modify cv.md or portfolio files
3. Submit applications on behalf of the candidate
4. Share phone number in generated messages
5. Recommend comp below market rate
6. Generate a PDF without reading the JD first
7. Use corporate-speak
8. Ignore the tracker (every evaluated offer gets registered)

### ALWAYS

0. **Cover letter:** If the form allows it, ALWAYS include one. Same visual design as CV. JD quotes mapped to proof points. 1 page max.
1. Read cv.md, config/profile.md, and article-digest.md (if exists) before evaluating
1b. **First evaluation of each session:** Run `node cv-sync-check.mjs`. If warnings, notify user.
2. Detect the role archetype and adapt framing per config/profile.md
3. Cite exact lines from CV when matching
4. Use WebSearch for comp and company data
5. Register in tracker after evaluating
6. Generate content in the language of the JD (EN default)
7. Be direct and actionable -- no fluff
8. Native tech English for generated text. Short sentences, action verbs, no passive voice.
8b. Case study URLs in PDF Professional Summary (recruiter may only read this).
9. **Tracker additions as TSV** -- NEVER edit applications.md directly. Write TSV in `batch/tracker-additions/`.
10. **Include `**URL:**` in every report header.**

### Tools

| Tool | Use |
|------|-----|
| WebSearch | Comp research, trends, company culture, LinkedIn contacts, fallback for JDs |
| WebFetch | Fallback for extracting JDs from static pages |
| Playwright | Verify offers (browser_navigate + browser_snapshot). **NEVER 2+ agents with Playwright in parallel.** |
| Read | cv.md, config/profile.md, article-digest.md, cv-template.html |
| Write | Temporary HTML for PDF, applications.md, reports .md |
| Edit | Update tracker |
| Canva MCP | Optional visual CV generation. Duplicate base design, edit text, export PDF. Requires `cv.canva_resume_design_id` in profile.yml. |
| Bash | `node generate-pdf.mjs` |

### Time-to-offer priority
- Working demo + metrics > perfection
- Apply sooner > learn more
- 80/20 approach, timebox everything

---

## Writing Style

A fixed, industry-standard voice for software-engineering job-seeker text. Apply it directly — there is no per-user sampling or calibration. (Per the profile-override rule above, a `## Writing Style` section in `config/profile.md` supersedes this if the user defines one.)

**When to apply:** Any text the user will send or publish — cover letters, LinkedIn outreach, application-form answers, follow-up emails, executive summaries, profile blurbs. Does NOT apply to internal evaluation reports (A–F blocks, scores, analysis).

### Tone & register
- Direct and confident, never boastful — let the results carry the confidence.
- Conversational and warm; peer-to-peer, not fawning or corporate.
- No hedging — cut "I think", "perhaps", "somewhat", "just", "kind of", "hopefully".
- Lead with what you built and the outcome, not with adjectives about yourself.

### Sentence structure
- Short, punchy sentences; vary the length so it doesn't read like a list.
- Active voice, first person: "I built", "I shipped", "I cut" — not "was responsible for".
- Bottom line up front: state the result, then the how.
- One idea per sentence, one theme per paragraph. Keep paragraphs to 2–4 sentences.

### Vocabulary
- Strong, specific verbs: built, shipped, designed, scaled, automated, migrated, debugged, owned, cut. Avoid weak fillers: leveraged, spearheaded, facilitated, utilized, "responsible for".
- Name the exact tech and tool — never gesture vaguely at "modern technologies".
- Quantify wherever the CV supports it: latency, throughput, %, users, time saved. A number beats a claim.
- Mirror the JD's own terminology and stack names (helps ATS keyword match and recruiter resonance) — don't swap them for synonyms.

### Punctuation & mechanics
- Full stops to separate ideas. Avoid comma splices and semicolons in short outreach.
- No exclamation marks. No ellipses. Don't overuse em dashes (they're ASCII-normalized downstream anyway).
- No Oxford comma.
- Standard sentence case — no ALL CAPS for emphasis, no emoji in formal documents.

### Authenticity
- Sound like a specific engineer, not a template. Avoid AI-generic phrasing and corporate filler.
- Show, don't tell: "reduced p95 latency 40%" beats "highly skilled at performance optimization".
- Avoid the cliché phrases listed under *Professional Writing & ATS Compatibility* below.

---

## Professional Writing & ATS Compatibility

These rules apply to ALL generated text that ends up in candidate-facing documents: PDF summaries, bullets, cover letters, form answers, LinkedIn messages. They do NOT apply to internal evaluation reports.

### Avoid cliché phrases
- "passionate about" / "results-oriented" / "proven track record"
- "leveraged" (use "used" or name the tool)
- "spearheaded" (use "led" or "ran")
- "facilitated" (use "ran" or "set up")
- "synergies" / "robust" / "seamless" / "cutting-edge" / "innovative"
- "in today's fast-paced world"
- "demonstrated ability to" / "best practices" (name the practice)

### Unicode normalization for ATS
`generate-pdf.mjs` automatically normalizes em-dashes, smart quotes, and zero-width characters to ASCII equivalents for maximum ATS compatibility. But avoid generating them in the first place.

### Vary sentence structure
- Don't start every bullet with the same verb
- Mix sentence lengths (short. Then longer with context. Short again.)
- Don't always use "X, Y, and Z" — sometimes two items, sometimes four

### Prefer specifics over abstractions
- "Cut p95 latency from 2.1s to 380ms" beats "improved performance"
- "Postgres + pgvector for retrieval over 12k docs" beats "designed scalable RAG architecture"
- Name tools, projects, and customers when allowed
