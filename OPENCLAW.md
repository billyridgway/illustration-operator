# OPENCLAW.md – Operating Playbook for `illustration-operator`

This file is for agents (and humans) using OpenClaw against this repo. It
lays out the main **loops** that matter for the Kubernetes operator.

---

## Loop 1 – Repo → Cluster Development Loop

**Goal:** Safely evolve the operator and roll changes onto the Raspberry Pi
k3s cluster.

**Default path:**

1. **Edit & test locally**
   - Update Go types/controllers in `api/` and `controllers/`.
   - Run:
     - `go test ./...`
     - `go vet ./...` when appropriate.
2. **Sync CRDs & manifests**
   - Ensure `config/crd/bases/illustrations.poc_illustrationprojects.yaml`
     matches the Go types (spec + status fields).
   - Keep sample YAML under `config/samples/` up‑to‑date.
3. **Commit & push**
   - Use clear commit messages that reference which CRD fields/behaviors
     changed.
4. **Build & push operator image** (via CI, once wired up)
   - Target tag pattern: `ghcr.io/<owner>/illustration-operator:<branch>`.
5. **Deploy to k3s**
   - Update the Deployment manifest with the new image tag.
   - Apply using `KUBECONFIG=.kube/pi-k3s.yaml`.
6. **Validate**
   - Create/patch an `IllustrationProject` sample.
   - Confirm Jobs, status fields, and MinIO objects behave as expected.

---

## Loop 2 – “Insurance Workflow Simulator” Loop

**Goal:** Safely wire new product/assumption features through the operator.

Typical cross‑cutting change: “Add a new product assumption field, update API
schema, UI, DB, operator CRD, sample YAML, and an integration test.”

For the **operator side**, expect to:

1. **Adjust product wiring**
   - Update `config/products.yaml` to include new DSL files, prefixes, or
     assumption IDs.
2. **CRD and status wiring**
   - Extend `IllustrationProjectSpec` or `IllustrationProjectStatus` with new
     fields that represent the wiring, not raw data (e.g. additional object
     keys, assumption IDs).
3. **Controller logic**
   - Update the reconciler to:
     - compute new prefixes or object names,
     - set new status fields,
     - pass new env vars/args into Jobs.
4. **Samples & tests**
   - Update `config/samples/*.yaml` to exercise the new fields.
   - Prefer adding small controller tests where practical.

You should think in terms of **end‑to‑end wiring** when a product change is
requested, not piecemeal edits.

---

## Loop 3 – Actuarial Audit / Trust Loop

**Goal:** Maintain a trustworthy, non‑leaky CRD surface that supports audit
and explanation.

For this repo, that means:

- Keep `status` focused on:
  - which product and assumptions were used,
  - which MinIO objects were created (projections, audit, input snapshot),
  - which runner image and engine version ran.
- Avoid ever putting raw PAS records or detailed projection values into CRD
  status.
- When you add new status fields, ask:
  - Does this help an actuary or engineer understand *what happened*?
  - Does this avoid exposing PII or sensitive data?

When coordinating with `actuarypoc`, you are responsible for **wiring trust
metadata through Kubernetes**, not computing it.

---

## Loop 4 – Product‑Management / Backlog Loop

**Goal:** Turn informal product ideas into concrete operator work items.

When you see backlog items like:

> Support SERFF document ingestion into object storage with source
> provenance and policy form versioning.

Ask how much belongs here vs. in `actuarypoc`:

- `actuarypoc`:
  - ingestion & parsing of docs,
  - tagging MinIO objects with provenance.
- `illustration-operator`:
  - product registry entries that reference those prefixes/docs,
  - status fields that point to the docs used for a run.

Then:

1. Draft **GitHub issues** in this repo for the operator‑specific work:
   - spec/status extensions
   - controller logic
   - samples.
2. Include acceptance criteria and a minimal test plan tied to:
   - a sample `IllustrationProject`, and
   - expected status / Job behavior in the dev cluster.

---

## Relationship to `AGENTS.md`

- `AGENTS.md` defines **rules and constraints** for working in this repo.
- `OPENCLAW.md` defines the **loops and workflows** that agents should run
  when making changes.

Agents should treat both as active constraints when operating on the
illustration operator.
