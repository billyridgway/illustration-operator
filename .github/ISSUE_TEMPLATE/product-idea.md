---
name: "Product idea (operator)"
about: "Capture operator work needed to support a new product or variation"
labels: ["product-idea"]
---

## Problem statement

Describe the product or configuration idea from an operator perspective:

- What new product/variant/rider needs orchestration support?
- What does the operator need to know (prefixes, DSL files, assumptions,
  PAS sources)?

## Acceptance criteria

- [ ] Desired changes to `products.yaml` are captured
- [ ] Any new env vars or Job wiring needs are documented
- [ ] Sample `IllustrationProject` YAML is drafted to exercise the new
      product

## Technical notes

- How will product wiring be represented (ProductDefinition vs `products.yaml`)?
- Do we need new status fields to describe the product wiring?
- Any MinIO prefix conventions that should be added/standardized?

## Test plan

- Manual test: apply the sample `IllustrationProject` and confirm Jobs run
  with correct env vars and prefixes
- Optional controller tests if behavior is complex

## Audit considerations

- Are there new artefact locations (projection/audit/input snapshot keys)
  that should be visible on CRD status?
- How should these runs be tied back to ProductDefinitions and filings in
  future audit views?
