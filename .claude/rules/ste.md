---
paths:
  - "docs/**/*.md"
  - "*.md"
  - ".claude/**/*.md"
  - "**/*.go"
  - "web/**/*.{js,html,css}"
  - "scripts/**"
---

# Writing standard (STE)

Every spec file, issue body, PR body, comment, manual page and **code comment** in this project uses Simplified Technical English. The controlled vocabulary is the `## Terms` table in `docs/specs/spec.md`. Read it before you write a domain word. If you need a word that is not in the table, add it to the table.

## What must not be rewritten

Reproduce these verbatim: quotes from a person, error messages, log and command output, test names, `file:line` references, code, config, commands, JSON, paths, identifiers, label names, third-party product names, API field names, and UI copy inside mockups.

## Rules

Sentences:
1. A procedure sentence is 20 words or fewer. A description sentence is 25 or fewer.
2. One instruction per sentence.
3. One topic per paragraph, six sentences at most.
4. Put the condition first, then the action.
5. Put the warning before the step it applies to.

Words:
6. One word, one meaning, one part of speech. In this project, `download` is a verb only. The noun is `job`.
7. One concept, one word. Never rotate synonyms.
8. Keep noun clusters to three words.
9. No metaphor, idiom or slang.
10. No abbreviation that the Terms table does not define.

Grammar:
11. Active voice.
12. Present tense for behavior. Imperative for instructions.
13. Keep the articles.
14. No `-ing` form as a noun or as a heading.
15. Write positively. Keep a prohibition only when the wrong action is likely and costly.
16. Use a vertical list when a sentence carries more than two conditions, parameters or alternatives.

## Patterns

- **Functional requirement:** one testable statement, active voice, present tense, no conjunction. Write "FR-x-3 — The engine retries a 503 response." Never write "should be able to".
- **Acceptance criterion:** the observable result, not the implementation. Never write "works correctly".
- **Issue title:** an imperative verb, one deliverable, 10 words or fewer.
- **Issue body:** `## Context`, `## What to build` (numbered steps), `## Acceptance criteria` (checkboxes), `## Out of scope`.
- **Code comment:** one sentence, one fact. Say why, not what. Name the reason a reader cannot see:
  ```go
  // ATAK flips tile_row on read, so the writer stores TMS rows.
  tmsY := (1 << z) - 1 - y
  ```
- **Doc comment:** open with one sentence that states the result. Then the parameters, then the failure modes.
  ```go
  // Open returns a writer for the output file at path.
  // It returns ErrExists when the file exists and mode is IfExistsFail.
  ```
- **Test name:** one behavior, present tense, for example `TestEngineSkipsTilesThatTheOutputFileHolds`, or `t.Run("rejects an HTML body with status 200", ...)`.
- **Marker comment:** keep `TODO`, `FIXME` or `HACK`. Write the body in STE and name the issue: `// TODO(#41): Read Retry-After as an HTTP date.`

## Check before you write the file

- [ ] No sentence is longer than 25 words, and no instruction is longer than 20.
- [ ] Every step holds one instruction.
- [ ] Every condition comes before its action, and every warning comes before its step.
- [ ] Every domain word is in the Terms table, with one meaning and one part of speech.
- [ ] No synonym rotation.
- [ ] No noun cluster longer than three words.
- [ ] No metaphor, idiom or undefined abbreviation.
- [ ] Active voice, present tense, articles present, and no `-ing` nouns or headings.
- [ ] Lists carry anything with more than two conditions or parameters.
- [ ] Quotes, evidence, code, paths and identifiers are verbatim.
- [ ] Code comments state the reason. Doc comments open with the result. Marker comments carry an issue number.
