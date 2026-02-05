# sandbox-base image

This image includes a minimal Ubuntu base plus `mise` for on-demand toolchain installs.

Build locally:
```bash
docker build -t sandbox-base:dev .
```

Load into kind:
```bash
kind load docker-image sandbox-base:dev --name sandbox-dev
```
