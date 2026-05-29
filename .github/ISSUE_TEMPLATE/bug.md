---
name: "Bug"
about: "Report a defect in the illustration operator (CRDs, controller, Jobs)"
labels: ["bug"]
---

## Problem statement

Describe the bug from the perspective of cluster behavior:

- What did you expect the operator to do (Jobs, status, CRDs)?
- What actually happened (missed Jobs, stuck status, wrong prefixes, etc.)?
- Which `IllustrationProject` and namespace are involved?

## Acceptance criteria

- [ ] Bug is reproduced and understood
- [ ] Fix is implemented and covered by tests (`go test ./...`)
- [ ] CRD/status behavior is well-defined and documented
- [ ] Any impact on MinIO object naming or Job wiring is validated

## Technical notes

- Suspected controller paths or types
- Relevant logs/events (redacted) from `kubectl describe` or `kubectl logs`
- Any correlation with specific MinIO prefixes or env vars

## Test plan

- Go unit/controller tests reproducing the bug where possible
- Manual k3s validation:
  - apply a sample `IllustrationProject`
  - observe Jobs, status, and MinIO artefacts

## Audit considerations

- Did this bug lead to incorrect or missing `projectionObject`/
  `auditObject`/`inputSnapshotObject` values in CRD status?
- Do we need to clarify how consumers should treat runs created while this
  bug was present?
