---
name: switchyard-production-operations
description: Operate, deploy, validate, or diagnose the real Switchyard production deployment on Kvant/NUC, including switchyard.service, Docker Compose, the isolated model ingress, authenticated operator API, Almaz/Authelia web boundary, upstream Ollama, and remote container reachability. Use for explicit Switchyard deployment requests, post-deploy proof, production incidents, or model-broker connectivity failures. Do not use for local development, generic repository review, or speculative production changes.
---

# Switchyard Production Operations

Treat production as a distinct target whose application contracts must be
proven from the relevant host and client.

## Source Order

1. Read `AGENTS.md` and `codemap.md`.
2. Read `deploy/codemap.md` before changing service installation or lifecycle.
3. Read `docs/runbooks/native-model-broker-cutover.md` for current topology,
   smoke commands, incident branches, and rollback.
4. Read `docs/development-readiness-report-2026-07-19.md` for historical
   qualification evidence only; never treat its commit or status as current.
5. Use [references/production-contract.md](references/production-contract.md)
   to select the required current evidence.

Repository documents win if topology or commands have changed.

## Workflow

1. **Resolve the real target.**
   - Establish host, checkout, commit, service unit, active Compose project,
     environment file, caller/client, and requested operation.
   - Do not infer production state from a local clone or an adjacent service.
2. **Inspect before mutation.**
   - Record `git status`, deployed revision, `systemctl` state, Compose state,
     listener ownership, and effective non-secret environment relevant to the
     incident.
   - Separate process, network, authentication, and application-contract
     evidence.
3. **Choose the operation.**
   - **Validate:** perform only read-only checks.
   - **Diagnose:** identify the first failing layer before proposing a fix.
   - **Deploy:** require an explicit deployment request, use the documented
     systemd/Compose path, and preserve local environment and unrelated changes.
   - **Rollback:** require explicit intent and follow the runbook; record which
     service owns each listener afterward.
4. **Verify from every relevant boundary.**
   - Check the control plane locally with required authentication.
   - Check model ingress from the Switchyard host and the affected remote
     container or host.
   - Check the public web route and expected Authelia redirect.
   - Prove listener isolation; a healthy container is not sufficient.
5. **Report evidence.**
   - State target, deployed revision, action, exact checks, results, safety
     contract, authentication boundary, remaining risks, and rollback status.

## Mutation Gates

- Do not deploy because a code change merely appears ready.
- Do not edit `.env`, firewall rules, proxy configuration, systemd units, or
  remote clients without resolving their actual current state and scope.
- Never print tokens, passwords, auth files, or complete secret-bearing
  environment output.
- Do not expose the operator control plane on the model-compatible listener.
- Do not stop a healthy production owner or invoke rollback during a forward
  test.

## Completion Contract

A deployment or repair is complete only when the intended revision is running
and the host, route, authentication, listener-isolation, model, and operator
safety contracts required by the operation all pass. Otherwise report the
first failing layer and stop short of a success claim.
