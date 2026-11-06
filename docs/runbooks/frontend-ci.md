# Frontend CI delivery

The `switchyard-check` job runs the full baseline `make verify`, builds the
frontend with its source revision and checks nginx's actual static responses.
Main pushes on Gitea then deliver that image through a restricted SSH key to the
existing `web` service on Kvant. Pull requests build and qualify without
deploying. Native production releases retain the qualification, backup,
restore and promotion requirements in [release and rollback](release-and-rollback.md).

This lane covers the existing dashboard contract and its selected Signal Yard
identity. The host admission contract pins backend and UI source objects,
dependency/build inputs, native container identities and start times, runtime
environment hashes, configuration files, the frontend's pinned bridge address
and the `open-pilot` runtime account. A source or native-state change requires a
compatibility review before updating admission. The deployment agent remains
`subcult-agent`; it does not replace the runtime identity.

The host owns `~/.config/fleet-deploy/switchyard-web.json`, its frontend-only
Compose baseline and the immutable admission hook. The forced command uses the
canonical `deploy/docker-local` boundary through a fixed host-owned wrapper.
It accepts only status, current-main frontend artifact transfer and deployment.
`HOMELAB_WEB_DEPLOY_KEY` and `HOMELAB_WEB_KNOWN_HOSTS` are repository secrets;
native credentials remain in their existing stores. The service-only baseline
retains the Tailscale listener, exact network address, environment and logging.
It does not recreate the network, native services or executor topology.

The image healthcheck requires native API health through the existing nginx
proxy and the local build marker. CI checks three uncached marker variants and
health from the admitted Almaz Tailscale peer, then checks the public route's
401 authentication challenge and authentication origin. The marker inherits
the existing IP admission rules; it creates no public bypass. Independent
activation proof compares served files with the deployed image and checks
native container, configuration and identity retention.

Private image/rollback receipts live under
`~/.local/state/fleet-deploy/switchyard-web/releases/`. Retain the prior frontend
image and service baseline. Application rollback selects only `web`; native
release pointers, service units, gate rules, credentials, queues and database
state stay under their existing owners. Frontend delivery creates no full
native-release qualification record.
