# CLAUDE.md

Guidance for Claude Code working in this repo.

## What this repo is

`trv-outpost-sim` — Go-based **data-source simulators** that feed test data to
the **Outpost dashboard** (`trv-enterprises/trv-outpost`). Services: ts-store,
WebSocket stream, REST API, PostgreSQL seeder, CSV server, plus writers
(`data-writer`, `sensor-readings-writer`). See `README.md` for endpoints.

**Split out of the dashboard repo's `dashboard/simulators/` on 2026-06-16**
(history preserved via `git subtree split`). It is now its own repo, checked
out flat as a sibling at `/Users/tviviano/Documents/GitHub/trv-outpost-sim`.

## How it's deployed (don't break this)

- Deployed via **`make deploy-simulators`** from the **`homelab-deploy`** repo,
  which runs a `trv-homelab` Ansible role that **syncs this repo's source**
  from `../trv-outpost-sim` (relative-sibling path in the role's
  `simulators_source` default).
- Target host: the `simulators` LXD container (`ssh root@100.70.88.83`), Docker.
- ts-store image is **pinned to `0.8.3`** in
  `homelab-deploy/inventory/host_vars/simulators.yml` — the role default is an
  OLD release, so a bare deploy without that pin would downgrade. Bump the
  host_var to upgrade.
- After changes here, redeploy with `make deploy-simulators` (run from
  `homelab-deploy`). The deploy is idempotent; store creation tolerates 409.

## Repo boundaries

- **Simulator services** (Go, Docker, compose) → **here**.
- **Dashboard UI components** (React, the globe/Sankey custom components) →
  **dashboard repo** (`trv-outpost`). `echarts-gl` is already a dep there.
- **Inventory / deploy / vault** → `homelab-deploy`. **Ansible roles/playbooks**
  → `trv-homelab`.

## Active plan

We are about to add a **network-traffic simulator** that replays the **AWS
Honeypot** dataset (real attacker IP + geo + target + volume) to drive a **3D
globe** (echarts-gl) and a **Sankey** in Outpost.

**Read `docs/TRAFFIC-SIM-PLAN.md` first** — it has the dataset decision, exact
schema, the spike to run before building, and open questions. The immediate
next step is the **data-shape spike** (fetch a sample, lock the schema, produce
real globe + Sankey echarts configs to eyeball) — NOT the full service yet.

## Conventions

- New simulator services follow the existing pattern: a small Go service with
  its own `Dockerfile`, wired into `docker-compose.yml`. Mirror `data-writer/`
  or `websocket/`.
- Get approval before architectural/directory changes or workarounds.
