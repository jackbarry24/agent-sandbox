FROM ubuntu:22.04

ENV DEBIAN_FRONTEND=noninteractive

RUN apt-get update \
    && apt-get install -y --no-install-recommends \
      ca-certificates curl git bash build-essential \
    && rm -rf /var/lib/apt/lists/*

# Runtime manager (mise) for on-demand toolchains.
RUN curl -fsSL https://mise.jdx.dev/install.sh | bash -s -- -y
ENV PATH="/root/.local/bin:$PATH"
RUN ln -s /root/.local/bin/mise /usr/local/bin/mise

# Cache locations (mount /cache as a writable volume).
ENV MISE_CACHE_DIR=/cache/mise/cache \
    MISE_DATA_DIR=/cache/mise/data \
    MISE_STATE_DIR=/cache/mise/state \
    NPM_CONFIG_CACHE=/cache/npm \
    PIP_CACHE_DIR=/cache/pip \
    CARGO_HOME=/cache/cargo \
    RUSTUP_HOME=/cache/rustup \
    GOMODCACHE=/cache/gomod \
    GOPATH=/cache/gopath \
    GOPROXY= \
    NPM_CONFIG_REGISTRY= \
    PIP_INDEX_URL= \
    CARGO_REGISTRIES_CRATES_IO_INDEX=

WORKDIR /workspace
CMD ["sleep", "infinity"]
