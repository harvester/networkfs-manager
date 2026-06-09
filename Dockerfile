FROM registry.suse.com/bci/golang:1.26 AS builder
ARG MK_HOST_ARCH
ENV ARCH=$MK_HOST_ARCH

RUN zypper -n rm container-suseconnect && \
    zypper -n install git curl docker gzip tar wget awk docker-buildx

COPY --from=golangci/golangci-lint:v2.12.2-alpine@sha256:91b27804074a0bacea298707f016911e60cf0cdbc6c7bf5ccacb5f0606d18d60 /usr/bin/golangci-lint /usr/local/bin/golangci-lint

RUN go install sigs.k8s.io/controller-tools/cmd/controller-gen@v0.17.1

RUN go install k8s.io/code-generator/cmd/openapi-gen@v0.29.13

ENV HOME=/go/src/github.com/harvester/networkfs-manager

# ---- base ----
FROM builder AS base
WORKDIR /go/src/github.com/harvester/networkfs-manager
COPY . .

# ---- build ----
FROM base AS build
ARG MK_REPO_ID

RUN --mount=type=cache,target=/go/pkg/mod,id=networkfs-manager-go-mod-${MK_REPO_ID} \
    --mount=type=cache,target=/go/src/github.com/harvester/networkfs-manager/.cache/go-build,id=networkfs-manager-go-build-${MK_REPO_ID} \
    ./scripts/build

FROM scratch AS build-output
COPY --from=build /go/src/github.com/harvester/networkfs-manager/bin/ /bin/

# ---- validate ----
FROM base AS validate
ARG MK_REPO_ID

RUN --mount=type=cache,target=/go/pkg/mod,id=networkfs-manager-go-mod-${MK_REPO_ID} \
    --mount=type=cache,target=/go/src/github.com/harvester/networkfs-manager/.cache/go-build,id=networkfs-manager-go-build-${MK_REPO_ID} \
    ./scripts/validate

# ---- validate-ci ----
FROM base AS validate-ci
ARG MK_REPO_ID

RUN git config --global user.email "ci@example.com" && \
    git config --global user.name "ci" && \
    git init 2>/dev/null && git add . && git commit -q -m "commit for validate-ci"

RUN --mount=type=cache,target=/go/pkg/mod,id=networkfs-manager-go-mod-${MK_REPO_ID} \
    --mount=type=cache,target=/go/src/github.com/harvester/networkfs-manager/.cache/go-build,id=networkfs-manager-go-build-${MK_REPO_ID} \
    ./scripts/validate-ci

# ---- test ----
FROM base AS test
ARG MK_REPO_ID

RUN --mount=type=cache,target=/go/pkg/mod,id=networkfs-manager-go-mod-${MK_REPO_ID} \
    --mount=type=cache,target=/go/src/github.com/harvester/networkfs-manager/.cache/go-build,id=networkfs-manager-go-build-${MK_REPO_ID} \
    ./scripts/test

# ---- generate-manifest ----
FROM base AS generate-manifest

RUN ./scripts/generate-manifest

FROM scratch AS generate-manifest-output
COPY --from=generate-manifest /go/src/github.com/harvester/networkfs-manager/manifests/ /manifests/
