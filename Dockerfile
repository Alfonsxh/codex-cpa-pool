ARG GO_BUILDER_IMAGE=docker.io/library/golang:1.25.0-alpine3.22@sha256:f18a072054848d87a8077455f0ac8a25886f2397f88bfdd222d6fafbb5bba440
ARG NODE_BUILDER_IMAGE=docker.io/library/node:22-alpine3.22@sha256:cd7807368cf24826297cbad5dca1a44972ccfd770647db52a8c7589eb4599ac8
ARG RUNTIME_IMAGE=docker.io/library/alpine:3.22@sha256:14358309a308569c32bdc37e2e0e9694be33a9d99e68afb0f5ff33cc1f695dce
FROM --platform=$BUILDPLATFORM ${GO_BUILDER_IMAGE} AS go-base
ARG GOPROXY=https://goproxy.cn
ARG GOSUMDB=sum.golang.google.cn
ARG TARGETOS
ARG TARGETARCH
ENV CGO_ENABLED=0 \
    GOOS=${TARGETOS} \
    GOARCH=${TARGETARCH} \
    GOPROXY=${GOPROXY} \
    GOSUMDB=${GOSUMDB}
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,id=cpap-go-mod,target=/go/pkg/mod \
    go mod download

FROM go-base AS control-builder
COPY cmd ./cmd
COPY internal ./internal
RUN --mount=type=cache,id=cpap-go-mod,target=/go/pkg/mod \
    --mount=type=cache,id=cpap-go-build,target=/root/.cache/go-build \
    mkdir -p /out \
    && for command in admin bootstrap collector failover log-maintenance notifications ownership quota releasectl; do \
         go build -tags timetzdata -trimpath -buildvcs=false -ldflags='-s -w' -o "/out/cpa-${command}" "./cmd/${command}"; \
       done

FROM go-base AS gateway-builder
COPY cmd/gateway ./cmd/gateway
COPY internal/gateway ./internal/gateway
COPY internal/reasoningpolicy ./internal/reasoningpolicy
RUN --mount=type=cache,id=cpap-go-mod,target=/go/pkg/mod \
    --mount=type=cache,id=cpap-go-build,target=/root/.cache/go-build \
    go build -tags timetzdata -trimpath -buildvcs=false -ldflags='-s -w' -o /out/cpa-gateway ./cmd/gateway

FROM go-base AS edge-builder
COPY cmd/edge ./cmd/edge
COPY internal/edge ./internal/edge
RUN --mount=type=cache,id=cpap-go-mod,target=/go/pkg/mod \
    --mount=type=cache,id=cpap-go-build,target=/root/.cache/go-build \
    go build -tags timetzdata -trimpath -buildvcs=false -ldflags='-s -w' -o /out/cpa-edge ./cmd/edge

FROM go-base AS web-go-builder
COPY cmd/web ./cmd/web
COPY internal/web ./internal/web
RUN --mount=type=cache,id=cpap-go-mod,target=/go/pkg/mod \
    --mount=type=cache,id=cpap-go-build,target=/root/.cache/go-build \
    go build -tags timetzdata -trimpath -buildvcs=false -ldflags='-s -w' -o /out/cpa-web ./cmd/web

FROM go-base AS test-upstream-builder
COPY cmd/test-upstream ./cmd/test-upstream
RUN --mount=type=cache,id=cpap-go-mod,target=/go/pkg/mod \
    --mount=type=cache,id=cpap-go-build,target=/root/.cache/go-build \
    go build -tags timetzdata -trimpath -buildvcs=false -ldflags='-s -w' -o /out/cpa-test-upstream ./cmd/test-upstream

FROM --platform=$BUILDPLATFORM ${NODE_BUILDER_IMAGE} AS web-builder
WORKDIR /src/frontend
COPY frontend/package.json frontend/package-lock.json ./
RUN --mount=type=cache,id=cpap-npm-cache,target=/root/.npm \
    npm ci --registry=https://registry.npmmirror.com
COPY frontend ./
RUN --mount=type=cache,id=cpap-npm-cache,target=/root/.npm \
    npm run build

# A verified release may replace this stage with its validated static output.
# Ordinary Docker builds continue to compile the frontend from source.
FROM scratch AS web-assets
COPY --from=web-builder /src/frontend/dist /dist

FROM ${RUNTIME_IMAGE} AS go-runtime
# Go embeds tzdata, but libc and shell tools also need the system zoneinfo.
ENV TZ=UTC
RUN sed -i 's|dl-cdn.alpinelinux.org|mirrors.tuna.tsinghua.edu.cn|g' /etc/apk/repositories \
    && apk add --no-cache tzdata \
    && ln -snf /usr/share/zoneinfo/UTC /etc/localtime \
    && test -s /etc/ssl/certs/ca-certificates.crt \
    && addgroup -S -g 10001 cpa \
    && adduser -S -D -H -u 10001 -G cpa cpa
LABEL org.opencontainers.image.source="https://github.com/Alfonsxh/codex-cpa-pool" \
      org.opencontainers.image.version="" \
      org.opencontainers.image.revision=""

FROM go-runtime AS gateway
COPY --from=gateway-builder /out/cpa-gateway /usr/local/bin/cpa-gateway
USER cpa:cpa
ENTRYPOINT ["/usr/local/bin/cpa-gateway"]

FROM go-runtime AS edge
COPY --from=edge-builder /out/cpa-edge /usr/local/bin/cpa-edge
USER cpa:cpa
ENTRYPOINT ["/usr/local/bin/cpa-edge"]

FROM go-runtime AS control
COPY --from=control-builder /out/cpa-admin /usr/local/bin/cpa-admin
COPY --from=control-builder /out/cpa-bootstrap /usr/local/bin/cpa-bootstrap
COPY --from=control-builder /out/cpa-collector /usr/local/bin/cpa-collector
COPY --from=control-builder /out/cpa-failover /usr/local/bin/cpa-failover
COPY --from=control-builder /out/cpa-log-maintenance /usr/local/bin/cpa-log-maintenance
COPY --from=control-builder /out/cpa-notifications /usr/local/bin/cpa-notifications
COPY --from=control-builder /out/cpa-ownership /usr/local/bin/cpa-ownership
COPY --from=control-builder /out/cpa-quota /usr/local/bin/cpa-quota
COPY --from=control-builder /out/cpa-releasectl /usr/local/bin/cpa-releasectl
CMD ["/usr/local/bin/cpa-admin"]

FROM go-runtime AS test-upstream
COPY --from=test-upstream-builder /out/cpa-test-upstream /usr/local/bin/cpa-test-upstream
USER cpa:cpa
ENTRYPOINT ["/usr/local/bin/cpa-test-upstream"]

FROM go-runtime AS web
COPY --from=web-go-builder /out/cpa-web /usr/local/bin/cpa-web
COPY --from=web-assets /dist/portal /srv/cpa-web/portal
COPY --from=web-assets /dist/admin /srv/cpa-web/admin
COPY --from=web-assets /dist/usage /srv/cpa-web/usage
LABEL org.opencontainers.image.source="https://github.com/Alfonsxh/codex-cpa-pool" \
      org.opencontainers.image.version="" \
      org.opencontainers.image.revision=""
USER cpa:cpa
ENTRYPOINT ["/usr/local/bin/cpa-web"]
