# syntax=docker/dockerfile:1.7

FROM node:24-alpine@sha256:d32cdf619f63fe0471182d08996dd516c6275bb5fd31ae06e55a570bd9e1ad43 AS frontend-build
WORKDIR /src/frontend
COPY frontend/package.json frontend/package-lock.json ./
RUN npm ci
COPY frontend/ ./
RUN npm run build

FROM golang:1.25.13-bookworm@sha256:e401dae1bf814e29204a8cb7915682e1780951e609ca0dd8865ee1937f510c48 AS go-build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ ./cmd/
COPY internal/ ./internal/
COPY web/ ./web/
COPY --from=frontend-build /src/web/dist ./web/dist
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/ops-deploy ./cmd/ops-deploy \
    && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/cicd-reconcile ./cmd/cicd-reconcile \
    && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/cicd-job-sync ./cmd/cicd-job-sync \
    && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/cicd-source-credential-sync ./cmd/cicd-source-credential-sync \
    && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/cicd-dockerfile-import ./cmd/cicd-dockerfile-import \
    && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/project-kubectl ./cmd/project-kubectl \
    && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/project-aws ./cmd/project-aws \
    && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/environment-eks-access-update ./cmd/environment-eks-access-update \
    && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/environment-scheduling-profile ./cmd/environment-scheduling-profile

FROM debian:trixie-slim@sha256:020c0d20b9880058cbe785a9db107156c3c75c2ac944a6aa7ab59f2add76a7bd AS tooling

ARG TARGETARCH
ARG TERRAFORM_VERSION=1.15.9
ARG KUBECTL_VERSION=1.35.0
ARG HELM_VERSION=4.1.4
ARG AWSCLI_VERSION=2.33.13

RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates curl unzip \
    && rm -rf /var/lib/apt/lists/* \
    && curl --proto '=https' --tlsv1.2 -fsSLo /tmp/terraform.zip "https://releases.hashicorp.com/terraform/${TERRAFORM_VERSION}/terraform_${TERRAFORM_VERSION}_linux_${TARGETARCH}.zip" \
    && curl --proto '=https' --tlsv1.2 -fsSLo /tmp/terraform.sha256 "https://releases.hashicorp.com/terraform/${TERRAFORM_VERSION}/terraform_${TERRAFORM_VERSION}_SHA256SUMS" \
    && grep "terraform_${TERRAFORM_VERSION}_linux_${TARGETARCH}.zip" /tmp/terraform.sha256 | sed "s#terraform_${TERRAFORM_VERSION}_linux_${TARGETARCH}.zip#/tmp/terraform.zip#" | sha256sum -c - \
    && unzip /tmp/terraform.zip -d /usr/local/bin \
    && curl --proto '=https' --tlsv1.2 -fsSLo /usr/local/bin/kubectl "https://dl.k8s.io/release/v${KUBECTL_VERSION}/bin/linux/${TARGETARCH}/kubectl" \
    && curl --proto '=https' --tlsv1.2 -fsSLo /tmp/kubectl.sha256 "https://dl.k8s.io/release/v${KUBECTL_VERSION}/bin/linux/${TARGETARCH}/kubectl.sha256" \
    && echo "$(cat /tmp/kubectl.sha256)  /usr/local/bin/kubectl" | sha256sum -c - \
    && curl --proto '=https' --tlsv1.2 -fsSLo /tmp/helm.tar.gz "https://get.helm.sh/helm-v${HELM_VERSION}-linux-${TARGETARCH}.tar.gz" \
    && curl --proto '=https' --tlsv1.2 -fsSLo /tmp/helm.sha256 "https://get.helm.sh/helm-v${HELM_VERSION}-linux-${TARGETARCH}.tar.gz.sha256sum" \
    && sed "s#helm-v${HELM_VERSION}-linux-${TARGETARCH}.tar.gz#/tmp/helm.tar.gz#" /tmp/helm.sha256 | sha256sum -c - \
    && tar -xzf /tmp/helm.tar.gz -C /tmp \
    && mv "/tmp/linux-${TARGETARCH}/helm" /usr/local/bin/helm \
    && aws_arch="${TARGETARCH}" \
    && if [ "${TARGETARCH}" = "amd64" ]; then aws_arch="x86_64"; elif [ "${TARGETARCH}" = "arm64" ]; then aws_arch="aarch64"; else exit 1; fi \
    && curl --proto '=https' --tlsv1.2 -fsSLo /tmp/awscliv2.zip "https://awscli.amazonaws.com/awscli-exe-linux-${aws_arch}-${AWSCLI_VERSION}.zip" \
    && unzip -q /tmp/awscliv2.zip -d /tmp \
    && /tmp/aws/install --bin-dir /usr/local/bin --install-dir /usr/local/aws-cli \
    && chmod 0755 /usr/local/bin/terraform /usr/local/bin/kubectl /usr/local/bin/helm \
    && rm -rf /tmp/* /var/lib/apt/lists/*

RUN mkdir -p /runtime/app/data \
        /runtime/app/environments \
        /runtime/app/terraform/infra/terraform.tfstate.d \
        /runtime/app/terraform/platform/terraform.tfstate.d \
        /runtime/home/ops-deploy \
        /runtime-root/usr/local/bin \
    && ln -s /usr/local/aws-cli/v2/current/bin/aws /runtime-root/usr/local/bin/aws \
    && chown -R 10001:10001 /runtime/app /runtime/home/ops-deploy

# AWS CLI v2 dynamically links zlib. Source the current library from a pinned
# Chainguard image and copy only that runtime object, not the development image.
FROM cgr.dev/chainguard/glibc-dynamic@sha256:14ef9ed852bfb33f8e20f56cebcea99c90bb2e50c1e083f9ceb4e0822c544c88 AS runtime-libs

# Keep download/build utilities out of the final image entirely. The pinned
# Chainguard runtime supplies only the glibc runtime and CA trust needed by the
# bundled AWS CLI and the deployment binaries.
FROM cgr.dev/chainguard/glibc-dynamic@sha256:7ff79e2caef2b8a137ddaf9940fb790e91148482092363760d6661e4591fd54c

ENV HOME=/home/ops-deploy \
    PATH=/usr/local/bin:/usr/bin:/bin \
    SSL_CERT_FILE=/etc/ssl/certs/ca-certificates.crt

WORKDIR /app
COPY --from=go-build /out/ops-deploy /usr/local/bin/ops-deploy
COPY --from=go-build /out/cicd-reconcile /usr/local/bin/cicd-reconcile
COPY --from=go-build /out/cicd-job-sync /usr/local/bin/cicd-job-sync
COPY --from=go-build /out/cicd-source-credential-sync /usr/local/bin/cicd-source-credential-sync
COPY --from=go-build /out/cicd-dockerfile-import /usr/local/bin/cicd-dockerfile-import
COPY --from=go-build /out/project-kubectl /usr/local/bin/project-kubectl
COPY --from=go-build /out/project-aws /usr/local/bin/project-aws
COPY --from=go-build /out/environment-eks-access-update /usr/local/bin/environment-eks-access-update
COPY --from=go-build /out/environment-scheduling-profile /usr/local/bin/environment-scheduling-profile
COPY --from=tooling /usr/local/bin/terraform /usr/local/bin/terraform
COPY --from=tooling /usr/local/bin/kubectl /usr/local/bin/kubectl
COPY --from=tooling /usr/local/bin/helm /usr/local/bin/helm
COPY --from=tooling /usr/local/aws-cli /usr/local/aws-cli
COPY --from=tooling /runtime-root/ /
COPY --from=runtime-libs /lib/libz.so.1* /lib/
COPY --from=tooling --chown=10001:10001 /runtime/app /app
COPY --from=tooling --chown=10001:10001 /runtime/home/ops-deploy /home/ops-deploy
COPY --chown=10001:10001 config.yaml ./config.yaml
COPY --chown=10001:10001 terraform/ ./terraform/

USER 10001:10001
EXPOSE 8080
ENTRYPOINT ["ops-deploy"]
CMD ["serve", "--config", "/app/config.yaml"]
