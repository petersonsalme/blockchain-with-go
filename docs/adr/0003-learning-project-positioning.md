# ADR-0003: Position the repository as a personal learning project with an AI-generated-changes disclaimer

Status: Accepted (2026-10-19)

## Context
- The repository is an old personal learning project about blockchain in Go.
- It is now also a pilot for evaluating Paperclip adoption with AI agents.
- The author works in the same industry, so the README must make clear that the project is not an employer's work or statement.

## Decision
- The README opens with a prominent disclaimer, in English, stating that the project:
  - is an educational, personal exercise;
  - is a pilot for Paperclip adoption with AI agents, and recent changes are purely AI-generated suggestions;
  - is not production-ready and must not hold or move real value;
  - is not affiliated with the author's employer, clients or any other organization.
- The README title is `Learning Blockchain with Go`.
- The GitHub About text uses the same positioning.

## Consequences
- Future README, About and documentation changes keep this positioning and do not present AI-suggested changes as vetted professional guidance.
- Changing the project's relationship to the author's employer, or ending the pilot, requires updating this ADR.
