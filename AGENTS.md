# Repository contribution rules

- Follow [CONTRIBUTING.md](CONTRIBUTING.md). Each PR has one independently explainable, testable behavior goal; multiple related commits are welcome.
- Start independent work from an updated upstream baseline. Do not include unrelated unpublished work from the fork's master branch.
- Use Conventional Commit subjects and PR titles. Keep incidental formatting, dependency changes, and unrelated refactors in separate PRs.
- Explain dependencies and actual verification. Review the complete `base...HEAD` diff before proposing a PR; size warnings do not prove semantic cohesion.
- Do not rewrite shared history, publish branches, or change remote repository settings without authorization. Local hook setup is optional and must preserve existing configuration.
