# Incident Response

## First response

Identify the real host, checkout, service, Compose project, caller, and affected
route. Do not infer production state from a local clone.

Inspect in this order:

1. deployed revision and worktree state;
2. service and container state;
3. application process and recent redacted logs;
4. listener ownership and reachability;
5. authentication and proxy assumptions;
6. readiness, operator safety, and model-broker contracts;
7. real upstream Ollama behavior;
8. affected remote-client behavior.

Stop at the first failed prerequisite.

## Initial alerts

The repository includes starter Prometheus rules in
`deploy/monitoring/switchyard-alerts.yml`. Before enabling them, assign an alert
receiver and escalation owner and validate the actual metric labels.

Initial thresholds:

- readiness unavailable for five minutes: critical;
- model-broker queue above 50 for ten minutes: warning;
- more than five worker failures in fifteen minutes: warning;
- newest successful PostgreSQL backup older than 26 hours: critical;
- host or PostgreSQL storage above 80%: warning and above 90%: critical;
- repeated canonical model-generation failure: critical.

Thresholds should be tuned from recorded capacity and traffic rather than
silently raised to suppress alerts.

## Alert test

For every alert, record:

- the deliberate test condition;
- firing time;
- receiver and escalation path;
- acknowledgement time;
- recovery notification;
- any missing runbook context.

Configuration without a delivered and acknowledged test alert is incomplete.

## Recovery

Use `release-and-rollback.md` for application or database rollback and
`native-model-broker-cutover.md` for model-ingress ownership. Never stop a
healthy listener owner as part of a forward test.
