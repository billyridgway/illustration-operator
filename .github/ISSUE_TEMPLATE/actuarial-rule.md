---
name: "Actuarial rule (operator impact)"
about: "Capture operator changes needed for a specific actuarial rule/assumption"
labels: ["actuarial-rule"]
---

## Problem statement

Describe the rule or assumption change and why the operator needs to change:

- What rule is changing (e.g. assumption set ID, mortality basis, risk class
  mapping)?
- How does that affect product wiring, Jobs, or `IllustrationProject`
  behavior?

## Acceptance criteria

- [ ] Operator wiring (env vars, Job parameters, status fields) is aligned
      with the new rule
- [ ] Any required CRD/spec changes are documented and implemented
- [ ] Sample CR and Jobs demonstrate the new wiring

## Technical notes

- Which fields in `IllustrationProjectSpec`/Status or `products.yaml` are
  impacted?
- Are new AssumptionSet IDs or ProductDefinition IDs introduced?
- Any compatibility concerns with existing runs or projects?

## Test plan

- Update or add tests around controller behavior for the rule
- Manual cluster test with a sample project using the new rule

## Audit considerations

- How will this rule change be visible via CRD status and MinIO object keys?
- Do we need to update documentation to guide downstream consumers (e.g.
  RunDetail/AuditRecord) about which assumption wiring is authoritative?
