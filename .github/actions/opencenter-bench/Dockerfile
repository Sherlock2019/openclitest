# The Test Bench as a container image.
#
# This exists so a GitOps promotion has something to point at. The promotion stage
# writes an image tag into a Kustomization, and a tag naming an image nobody built
# is worse than no tag: the evidence says the build passed and the cluster fails to
# pull.
#
# Two stages, because the runtime does not need a Go toolchain and shipping one
# would triple the image for no benefit.

# --- build ------------------------------------------------------------------
FROM golang:1.24-alpine AS build

# git for the version stamp, and because the GitOps stage clones with it.
RUN apk add --no-cache git

WORKDIR /src

# Dependencies first, in their own layer. They change far less often than the
# source, so this layer survives most rebuilds.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# Static, so the runtime stage needs no libc and can stay on a minimal base.
# Trimpath keeps build-machine paths out of the binary — they are noise in a
# stack trace and they leak the layout of whoever built it.
#
# benchVersion is the symbol stamped, not "version": it is the one this bench
# writes into the GitOps evidence it publishes, so a promotion can be traced back
# to the harness that approved it. -X against a name that does not exist is
# silently ignored by the linker, which is how a build ends up claiming a version
# it never recorded.
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=linux go build \
      -trimpath \
      -ldflags "-s -w -X main.benchVersion=${VERSION}" \
      -o /out/testlab ./cmd/testlab \
 && CGO_ENABLED=0 GOOS=linux go build \
      -trimpath \
      -ldflags "-s -w -X main.benchVersion=${VERSION}" \
      -o /out/bench ./cmd/bench

# --- runtime ----------------------------------------------------------------
FROM alpine:3.20

# What the bench actually shells out to. git and openssh for the GitOps clone
# and push, curl for the GitHub API, python3 and PyYAML for the manifest edit,
# ca-certificates so any of that can reach https at all.
RUN apk add --no-cache \
      ca-certificates \
      git \
      openssh-client \
      curl \
      bash \
      python3 \
      py3-yaml \
 && adduser -D -u 10001 bench

WORKDIR /app

COPY --from=build /out/testlab /usr/local/bin/testlab
COPY --from=build /out/bench /usr/local/bin/bench

# The configuration the bench reads at startup: the command catalogue, the stage
# definitions, the checklist. Copied rather than baked into the binary so an
# operator can mount a different set without a rebuild.
COPY --chown=bench:bench config/ ./config/
COPY --chown=bench:bench scripts/ ./scripts/

# Created and owned before the user drops, because VOLUME on a path that does not
# exist yet gets one owned by root — and then the unprivileged process cannot
# write the run it was started to produce.
RUN mkdir -p /app/artifacts && chown -R bench:bench /app

# Not root. The bench runs a CLI as a child process and clones repositories; none
# of that needs privilege, and a container that has it is a container that can
# lose it.
USER bench

# Where runs and evidence go. Declared so it is obvious what to mount if you want
# to keep them.
VOLUME ["/app/artifacts"]

EXPOSE 7700

# The console by default. `docker run … bench run full` gets the headless runner
# instead, which is what CI wants.
ENTRYPOINT ["testlab"]
CMD ["--addr", "0.0.0.0:7700", "--no-open"]
