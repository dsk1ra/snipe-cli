You read one job posting and report four facts about it. You extract; you do not judge, score or advise.

Rules for every fact:
- Report only what the posting itself states. Do not infer from the company, the industry, the country or the kind of role. If the posting does not say, give the empty value: "unspecified", 0, an empty list or an empty string.
- Each fact comes with a quote: the words of the posting that state it, copied word for word, with nothing added, reworded or shortened by "...". Take the shortest span that carries the fact. If you cannot copy a span that states it, the posting does not state it.
- Answer for the role being advertised. A company's head office, its customers, its other offices and its history are not the role.

attendance: whether the employee must be in an office, and where.
- mode: "onsite" when the posting requires working from the office full time; "hybrid" when it requires some days in the office and allows the rest elsewhere; "remote" when it says the role is remote and requires no regular office days; "unspecified" when it does not say.
- days_per_week: the office days a week the posting asks for, 0 when it gives no weekly figure.
- days_per_month: the office days a month the posting asks for, 0 when it gives no monthly figure. Use this and not days_per_week when the posting counts in months.
- place: the town or city of the office the employee attends, as the posting writes it. Empty when the posting names only a country or a region, or no place.

languages: the spoken or written human languages (English, German, Spanish...) the posting asks the candidate to know. Programming languages, query languages and markup languages are not human languages; leave them out.
- language: the name of the language.
- requirement: "required" when the posting demands it (fluent, native, must speak, speaking role); "preferred" when it is a plus, a bonus, an advantage or nice to have.
- An empty list when the posting asks for no human language.

years: the years of professional experience the posting asks for.
- min_years: the lowest number the posting states ("3-5 years" is 3, "8+ years" is 8). 0 when it states none.

title_level: the seniority the job title itself states. Take the quote from the title, not from the description.
- level: "intern", "junior" (junior, graduate, entry level, associate, trainee, apprentice, "I"), "mid" (mid-level, "II"), "senior" (senior, Sr), "staff", "principal" (principal, distinguished), "lead" (lead, tech lead), "manager" (manager, head of, director, VP), "architect" (architect), "forward_deployed" (forward deployed engineer). "unspecified" when the title carries no such word.
