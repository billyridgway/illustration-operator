# AGENTS.md – Illustration Operator Repo Operator Rules

This file defines how the local OpenClaw agent should behave when working in
this repo. Treat it as the operator’s rulebook, not just a note.

## 0. Role of this repo

`illustration-operator` is the **Kubernetes controller** that turns
`IllustrationProject` CRDs into:

- MinIO object keys for projections, audits, and input snapshots
- Kubernetes Jobs that invoke `actuarypoc` CLIs
- Status fields that UIs and tools can watch

The Raspberry Pi k3s cluster with MinIO is the default dev environment.

---

## 1. Repo → Cluster development loop

When making changes here, follow this loop:

1. **Edit + build locally**
   - Update Go types/controllers/config.
   - Run at least:
     - `go test ./...`
   - Ensure `go build ./...` passes.
2. **Update CRD + manifests**
   - If types under `api/v1alpha1` change:
     - update `config/crd/bases/*.yaml` accordingly,
     - keep status fields aligned with what the controller writes.
3. **Commit + push to GitHub**
   - Target `main` or a coordinated feature branch.
4. **Build operator image via GitHub Actions** (to be wired similarly to
   `actuarypoc` if not already):
   - Build/push `ghcr.io/<owner>/illustration-operator:<branch>`.
5. **Deploy to k3s**
   - Update the operator Deployment YAML (image tag/env) under `config/` or
     your cluster‑level manifests.
   - Apply using the provided kubeconfig.
6. **Validate against the cluster**
   - Create or update an `IllustrationProject` sample
     (e.g. `config/samples/p12trf-serff-demo.yaml`).
   - Confirm Jobs and status fields behave as expected.

---

## 2. Kubernetes changes – required steps

For **any change that affects CRDs or controller behavior**, do all of these:

1. **Update CRD types**
   - Edit Go types in `api/v1alpha1/illustrationproject_types.go`.
   - Make sure JSON tags, defaults, and comments match intended behavior.

2. **Update generated manifests**
   - Keep `config/crd/bases/illustrations.poc_illustrationprojects.yaml`
     consistent with the Go types:
     - all new status/spec fields represented,
     - removed fields cleaned up.

3. **Add or refresh sample YAML**
   - Update samples under `config/samples/` to exercise new fields/paths.
   - At least one sample should be runnable on the dev cluster
     (namespace, productId, pasConfigMap, etc.).

4. **Run a dev deploy**
   - Apply updated CRD + Deployment to the k3s cluster.
   - Reconcile at least one `IllustrationProject` end‑to‑end.

If a change would break older CRD instances, document the migration path in
`README.md` or a short `docs/` note.

---

## 3. Contract with `actuarypoc` image

The operator must **not** hard‑code behavior that drifts away from the
`actuarypoc` image contract.

When changing env vars or CLI usage for Jobs:

- Keep all `EnvVar` + CLI flags in `controllers/illustrationproject_controller.go`
  in sync with `actuarypoc`’s `cli.main` entrypoints.
- If you introduce a new env var or change semantics:
  - adjust both the Go controller and the Python CLI,
  - add a note in either repo’s `README.md`.

For runner Jobs:

- Job names should remain deterministic (e.g. `illustration-<project>` and
  `assumptions-<product>`), unless there is a clear migration plan.
- MinIO prefixes and object keys should remain predictable and be surfaced in
  status where possible.

---

## 4. Trust / audit loop responsibilities

This repo is responsible for **wiring** trust and audit, not doing math.
When editing it, the agent should:

- Preserve or improve:
  - `status.assumptionSetId` / `status.assumptionApproved`
  - `status.projectionObject`, `status.auditObject`, `status.inputSnapshotObject`
  - `status.resolved` wiring summaries.
- Never leak policyholder‑level data into status; only metadata and object
  keys are allowed.

If you add new status fields, ask:

- Does this improve explainability and reproducibility?
- Does it avoid exposing raw PAS or projection values in the CRD status?

---

## 5. Product‑management support

When Billy uses chat to describe a desired behavior (e.g. "support SERFF
document ingestion into object storage with source provenance and policy form
versioning"), the agent should be ready to:

- Draft or update **GitHub issues** in this repo with:
  - clear problem statement
  - how it surfaces in `IllustrationProject` spec/status
  - acceptance criteria and minimal test plan.
- Keep a mental map of:
  - what belongs in `illustration-operator` (CRDs, controllers, Jobs), vs.
  - what belongs in `actuarypoc` (projection logic, assumptions, UI).

---

## 6. Coordination with the ActuaryPOC repo

When a change spans both repos (e.g. a new assumption field or product
parameter):

- Update `actuarypoc` first (data model + CLIs + tests),
- Then update `illustration-operator` to wire it through:
  - config/products.yaml
  - env vars / prefixes in the Jobs
  - status fields / resolved wiring summaries.

Never change operator behavior to rely on an `actuarypoc` feature that does
not yet exist on the image tag referenced by the Deployment.
