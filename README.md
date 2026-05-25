# Illustration Operator (POC)

Kubernetes operator for managing `IllustrationProject` CRDs and driving
illustration runs (ingestion + projection) against the Actuary POC stack
(MinIO + actuarypoc repo).

The operator turns a small, declarative CRD into concrete illustration
pipelines: it resolves product wiring, kicks off the necessary Jobs, and
surfaces status and artefact locations back on the `IllustrationProject`.

---

## Scope

This is a **proof‑of‑concept** operator focused on a narrow but realistic
scenario:

- **Cluster scope**: Namespaced operator intended for a single POC
  environment (MinIO + `actuarypoc`). It is *not* hardened for
  multi‑tenant or production use.
- **What it manages**
  - `IllustrationProject` CRDs (API group: `illustrations.poc/v1alpha1`).
  - One Kubernetes `Job` per project to run the illustration pipeline.
  - Optional one‑shot `Job` per product to run LLM‑based assumption
    extraction before projections.
- **What it assumes exists**
  - A product registry mounted at `/config/products.yaml` (usually via
    ConfigMap) that defines products and MinIO prefix conventions.
  - A runner image (set via `ILLUSTRATION_RUNNER_IMAGE`) that contains the
    `actuarypoc` CLI and its dependencies.
  - A MinIO instance reachable at
    `minio.minio-system.svc.cluster.local:9000` with the `illuminet` bucket
    and demo credentials (`admin` / `password`).
  - An `openai-api-key` Secret (key: `api-key`) when LLM extraction is
    enabled for a product.
- **Out of scope for this POC**
  - Production‑grade security, multi‑cluster rollout, and observability.
  - Full quoting / illustration UX; this is a backend pipeline driver.
  - Generalised scheduling/orchestration; `runPolicy` is currently
    informational only.

---

## What the operator does (function and flow)

At a high level, the reconciler does the following whenever an
`IllustrationProject` is created or updated:

1. **Load the project**
   - Read the `IllustrationProject` resource (spec + status).
   - Short‑circuit if the resource is being deleted.
2. **Resolve product wiring**
   - Load the product registry from `/config/products.yaml`.
   - Look up `spec.productId` to find the matching product config
     (DSL file, mortality hints, MinIO prefix base, optional LLM config).
   - Derive MinIO prefixes by convention:
     - `filingsPrefix`: `filings/<prefixBase>/`
     - `policiesPrefix`: `<prefixBase>/`
     - `projectionsPrefix`: `projections/<prefixBase>/`
   - Compute LLM‑related identifiers when configured (doc prefix and
     assumption ID), falling back to sensible conventions when omitted.
   - Populate `status.resolved` with a human‑readable summary of:
     product id, PAS source, DSL file, assumption set id, and prefixes.
3. **Optionally run LLM assumptions extraction (with approval gate)**
   - If the product has LLM config, ensure a one‑shot `Job` named
     `assumptions-<productId>` exists.
   - That Job runs the `actuarypoc` CLI to read the latest doc from MinIO
     (under the configured doc prefix) and upsert an `AssumptionSet` into
     the registry.
   - Once the Job completes successfully, the operator records the logical
     assumption id on status (`status.assumptionSetId`) and moves the
     project into the `AwaitingApproval` phase until a separate approval
     step marks `status.assumptionApproved = true`.
4. **Run the illustration pipeline (run‑oriented)**
   - After approval (or when no LLM refresh is configured), ensure a `Job`
     named `illustration-<projectName>` exists.
   - The Job uses the runner image to run
     `python -m actuarypoc.cli.main project-minio` against existing MinIO
     inputs; demo/bootstrap data is loaded by separate Jobs/flows, not as
     part of this Job.
   - The operator passes deterministic, run‑specific object keys into the
     container so it writes to:
     - `projections/<productId>/<runId>/projection.json`
     - `audit/<productId>/<runId>/audit.json` (sanitized audit metadata)
     - `audit/<productId>/<runId>/inputs.json` (input snapshot metadata)
   - If `spec.pasConfigMap` is set, the Job mounts that ConfigMap and points
     the projection service at PAS JSON on disk instead of MinIO
     `pas_export/` prefixes.
5. **Update status (sanitized wiring + artefact metadata)**
   - Before creating the Job, the operator writes the planned
     `status.projectionObject`, `status.auditObject`, and
     `status.inputSnapshotObject`, sets `status.engineVersion` (from
     environment) and `status.runnerImage`, and moves `status.phase` to
     `Running`.
   - While the Job is Pending/Running, it keeps `phase = Running` and
     updates `status.lastRunId`.
   - On Job completion or failure, it sets `phase` to `Succeeded` or
     `Failed`, updates `lastRunTime`, `lastRunId`, `lastError`, and leaves
     the artefact object keys pointing at the run‑specific locations. No
     PAS records, SERFF text, or raw projection values are written into
     status.

The net effect: a small CRD instance (“run an illustration for product X,
with these PAS inputs, over this horizon”) turns into concrete work
(assumption extraction + projection) plus traceable status in‑cluster.

---

## Background and motivation

This operator exists to glue together the **Actuary POC stack** into a
Kubernetes‑native workflow:

- The POC already has:
  - A product DSL + interpreter (`actuarypoc` repo).
  - MinIO as the object store for filings, PAS exports, and projections.
- What was missing was a **cluster‑native API** for “run this illustration
  scenario” that:
  - Can be versioned, watched, and reconciled like any other Kubernetes
    resource.
  - Encodes product knowledge (prefixes, DSL file, assumptions wiring)
    via a registry instead of per‑run ad‑hoc config.
  - Allows UIs or other services to simply `kubectl apply` an
    `IllustrationProject` and watch status, instead of wiring directly to
    MinIO or a separate orchestrator.

Design choices in this POC:

- Use **CRDs + controller‑runtime** rather than a bespoke API server.
- Keep the **CRD spec minimal** (product id, horizon, mode, PAS source,
  run policy, notes) and push most wiring into a product registry and
  naming conventions.
- Use **Kubernetes Jobs** to execute both LLM extraction and projections,
  so that retry/backoff, logs, and resource limits are all first‑class.
- Hard‑code sensible defaults for a single POC cluster (MinIO endpoint,
  bucket, demo credentials, paths inside the runner image), with the
  expectation that a production version would push these into ConfigMaps
  and Secrets.

---

## Project layout

This project follows the layout generated by Operator SDK / Kubebuilder:

- Go types for the CRD under `api/v1alpha1`.
- Controller implementation under `controllers/`.
- CRD YAML and deployment manifests under `config/`.

The code is written to be compatible with Operator SDK v1+
(controller‑runtime based). You can later re‑run `operator-sdk init` /
`operator-sdk create api` if you want a fresh scaffold; the types and
controller here should drop into that layout cleanly.

---

## CRD overview

`IllustrationProject` (short name: `ilproj`) is namespaced, intentionally
minimal, and run‑oriented. For the POC, one `IllustrationProject` typically
represents one illustration run/project.

Spec remains deliberately small:

- `productId` – logical product identifier (must exist in the product
  registry).
- `horizonYears` – projection horizon in years.
- `mode` – `adhoc` now; `scheduled` reserved for future scheduler work.
- `pasConfigMap` – optional ConfigMap providing PAS JSON under `pas.json`.
- `runPolicy` – optional hints for a future scheduler (cron, concurrency).
- `notes` – free‑form text for humans; ignored by the controller logic.

Example:

```yaml
apiVersion: illustrations.poc/v1alpha1
kind: IllustrationProject
metadata:
  name: p12trf-serff-demo
  namespace: illustrations-poc
spec:
  productId: p12trf           # must exist in the product registry
  horizonYears: 40            # projection horizon (default: 40)
  mode: adhoc                 # adhoc | scheduled (scheduled is future work)
  pasConfigMap: ""            # optional; ConfigMap with PAS JSON under pas.json
  notes: "Demo project for P12TRF SERFF filings"
```

The product registry lives in `config/products.yaml` and is typically
mounted into the operator at `/config/products.yaml`.

Status is where most of the interesting runtime information lives. Key
status fields (all sanitized / metadata only):

- `phase` – coarse lifecycle: `Pending`, `Running`, `AwaitingApproval`,
  `Succeeded`, `Failed`.
- `lastRunId` / `lastRunTime` / `lastError` – recent run metadata.
- `projectionObject` – MinIO key for the projection JSON artefact.
- `auditObject` – MinIO key for a sanitized audit JSON artefact.
- `inputSnapshotObject` – MinIO key for a small input snapshot JSON
  (object keys, counts, timestamps).
- `assumptionSetId` – logical id of the AssumptionSet the run is wired to.
- `assumptionApproved` – whether that AssumptionSet has been explicitly
  approved for use (gates projections when LLM refresh is enabled).
- `engineVersion` – projection engine version used for the last run
  (string from environment).
- `runnerImage` – container image used for the illustration Job.
- `resolved` – structured summary of wiring (product id, PAS source,
  DSL file, MinIO prefixes) with no raw data.

---

## Next steps (outside this repo)

- Build a container image that includes this operator binary.
- Build a dedicated runner image for the illustration Jobs (with
  `actuarypoc` and its Python dependencies) and set `ILLUSTRATION_RUNNER_IMAGE`.
- Deploy with the usual Operator SDK manifests (Deployment + RBAC), using a
  ServiceAccount with access to the `IllustrationProject` CRD and related
  Jobs, ConfigMaps, and Secrets.
- Gradually factor out hard‑coded demo assumptions (sample data loads,
  MinIO creds) into proper configuration.
- Consider layering a higher-level orchestrator on top (if needed) once
  the desired pipeline shape is stable.
