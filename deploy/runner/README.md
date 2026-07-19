# Gitea Runner Image

The `switchyard-production` runner label uses this image. Both base images are
pinned by digest; publish the built image to a registry reachable by the runner's
Docker daemon and configure the label with the published manifest digest.

```bash
docker build -f deploy/runner/Dockerfile -t "$RUNNER_IMAGE" .
docker push "$RUNNER_IMAGE"
```

The runner remains responsible for mounting its Docker socket so the production
qualification gate can exercise Compose, Buildx, SBOM, and provenance behavior.
