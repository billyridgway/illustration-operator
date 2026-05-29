---
name: "Feature"
about: "Propose a new feature or enhancement for the illustration operator"
labels: ["feature"]
---

## Problem statement

Describe the user or system behavior this feature should enable:

- What workflow or capability is missing from the operator (CRDs, Jobs,
  status, wiring)?
- Which repos or components does it touch (operator only, also `actuarypoc`,
  k3s manifests)?

## Acceptance criteria

- [ ] New behavior is fully described at the CRD / status / Job level
- [ ] `go test ./...` passes with tests that cover the new behavior
- [ ] CRD manifests in `config/crd/bases/` match the Go types
- [ ] Sample YAML in `config/samples/` illustrates the new feature

## Technical notes

- Affected types (e.g. `IllustrationProjectSpec`, `IllustrationProjectStatus`)
- Expected changes to Jobs (env vars, args, labels, annotations)
- Any interactions with ProductDefinitions or FilingRecords in future

Do **not** include secrets or raw PAS/projection data. Reference names/keys
only.

## Test plan

- Go unit tests and controller tests
- Manual test using `kubectl apply -f config/samples/...` against the dev
  cluster
- Validation that new status fields or Jobs behave as expected in k3s

## Audit considerations

- Does this change how assumptions, product wiring, or artefact locations are
  surfaced on CRD status?
- Do we need to update docs to reflect any new status fields (for AuditRecord
  or RunDetail consumers)?
