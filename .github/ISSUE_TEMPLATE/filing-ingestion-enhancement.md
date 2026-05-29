---
name: "Filing ingestion enhancement (operator)"
about: "Operator changes related to SERFF/Filing ingestion and provenance"
labels: ["filing-ingestion"]
---

## Problem statement

Describe how filing ingestion or provenance affects operator behavior:

- What new information from FilingRecords/ProductDefinitions should the
  operator use (e.g. doc prefixes, assumption IDs)?
- Is this about configuring Jobs for extraction, projection, or both?

## Acceptance criteria

- [ ] CRD/spec changes to surface any new filing/provenance hints are
      documented and implemented
- [ ] Operator logic uses FilingRecord/ProductDefinition info consistently
- [ ] Sample projects show the expected behavior and artefact wiring

## Technical notes

- Fields in ProductDefinition or FilingRecord that the operator should
  consume
- Mapping from those fields into Job env vars or config maps
- Impact on existing Jobs and MinIO prefixes

## Test plan

- Unit/controller tests to exercise new wiring
- Manual dev-cluster test using a project that relies on filing/provenance
  hints

## Audit considerations

- How will these enhancements improve the clarity of `IllustrationProject`
  status and downstream audit views?
- Any risk of exposing sensitive filing details on CRD status that should be
  avoided?
