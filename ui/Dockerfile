# Dockerfile for the ShowMesh Operator UI (ADR-014, ADR-015, ADR-022).
#
# Multi-stage: a Node build stage produces static assets only; the runtime
# stage is nginx:alpine with no Node in it at all. The UI is a client of
# the coordinator's public API (ADR-014) and its own container per ADR-012;
# updating or removing this container must never touch the coordinator.
#
# Build context is this directory (ui/), not the repository root: the
# generated API types (src/api/generated/schema.d.ts) are committed to the
# repo per spec section 4.2 rather than regenerated at image-build time, so
# this build needs nothing from outside ui/. CI's ui-gen-check target
# (Makefile) is what proves that committed file still matches
# api/openapi.yaml; this Dockerfile trusts it rather than re-deriving it.

# node:22-alpine pins an LTS major without a floating "lts" tag drifting
# under CI; bump deliberately alongside the toolchain in package.json.
FROM node:22-alpine AS build

WORKDIR /app

# Copy only the dependency manifests first so `npm ci` is cached as its
# own layer and is only invalidated when dependencies change, not on
# every source edit (same reasoning as the coordinator's Dockerfile).
COPY package.json package-lock.json ./
RUN npm ci

COPY . .
RUN npm run build

# nginx:1.27-alpine pins a version rather than the floating "alpine" tag,
# consistent with the coordinator Dockerfile's pinning rationale.
FROM nginx:1.27-alpine

# nginx.conf is a template, not the final config: the coordinator upstream
# host and port are substituted at container start (CMD below), not baked
# in at build time. See nginx.conf for why a literal proxy_pass would
# refuse to start this container when the coordinator is absent.
COPY nginx.conf /etc/nginx/conf.d/default.conf.template
COPY --from=build /app/dist /usr/share/nginx/html

# Defaults match the coordinator's Compose service name and port
# (deploy/docker-compose.yml) so the bundled stack needs no overrides; an
# external coordinator changes only these two variables.
ENV SHOWMESH_COORDINATOR_HOST=coordinator
ENV SHOWMESH_COORDINATOR_PORT=8080

EXPOSE 8080

# /healthz is served entirely by nginx itself (see nginx.conf) and never
# contacts the coordinator: a coordinator outage must not report the UI
# container itself unhealthy (ADR-022, OPERATOR-UI section 3 isolation
# contract). wget is the busybox build already present in nginx:alpine, so
# no extra package install is needed for the healthcheck.
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
    CMD wget -q -O- http://127.0.0.1:8080/healthz || exit 1

# envsubst ships in the nginx image for its own template mechanism; it is
# invoked here directly and restricted to exactly these two variable names
# so that nginx's own "$"-prefixed runtime directives (proxy_pass targets,
# $http_upgrade, $uri, and so on) pass through untouched. The base image's
# built-in /etc/nginx/templates mechanism substitutes *every* variable
# currently in the container's environment, which is not used here for
# that reason — the template lives outside /etc/nginx/templates so that
# automatic pass never runs.
CMD ["/bin/sh", "-c", "envsubst '${SHOWMESH_COORDINATOR_HOST} ${SHOWMESH_COORDINATOR_PORT}' < /etc/nginx/conf.d/default.conf.template > /etc/nginx/conf.d/default.conf && exec nginx -g 'daemon off;'"]

LABEL org.opencontainers.image.source="https://github.com/ShowMeshSystems/showmesh" \
      org.opencontainers.image.licenses="Apache-2.0" \
      org.opencontainers.image.title="showmesh-operator-ui" \
      org.opencontainers.image.description="ShowMesh Operator UI: read-only TypeScript SPA client of the coordinator's public control API."
