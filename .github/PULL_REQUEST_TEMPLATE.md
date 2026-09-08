<!--
PRs that do not fill every section below will be sent back.
Source of truth: the Motivra build directive, "PR REQUIREMENTS".
-->

## Problem
<!-- What is broken, missing, or being built? Link the issue: Closes #NN -->

## Solution
<!-- What did you do? Keep it concrete — reviewers read this first. -->

## Architecture impact
<!-- Bounded contexts touched, contracts affected, new dependencies (with justification), ADR references. -->

## Database impact
<!-- Tables touched, migration strategy (expand/migrate/contract), rollback plan. Write "none" if truly none. -->

## API changes
<!-- New/changed endpoints or schemas. Breaking changes: versioning + compatibility plan. Write "none" if truly none. -->

## Events
<!-- New or changed domain events (name, schema_version). Write "none" if truly none. -->

## Security implications
<!-- AuthN/AuthZ, tenant isolation, validation, secrets, PII/evidence handling, abuse vectors considered. -->

## Testing
<!-- Unit / integration / contract / E2E coverage added. Offline-sync and failure-path tests where applicable. -->

## Observability
<!-- Metrics, traces, logs, alerts added. SLO impact. -->

## Performance considerations
<!-- Expected load, hot paths, indexes, caching. Measure, don't guess. -->

## Rollback strategy
<!-- How this change is safely reverted or feature-flagged in production. -->

## Screenshots / recordings
<!-- Required for UI changes. -->

## Checklist
- [ ] Existing architecture respected; no ownership boundary violated
- [ ] Tests written and passing (CI green)
- [ ] API/event contracts documented
- [ ] Migration tested (if any) with rollback path
- [ ] Security reviewed; no secrets committed
- [ ] Offline behavior considered where applicable
- [ ] Documentation updated; no unrelated files modified
