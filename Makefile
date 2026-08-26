CONFIG ?= config.yaml
PROJECT ?= ops
ENVIRONMENT ?= test
BINARY ?= bin/ops-deploy
PLATFORM_DEPLOY_BINARY ?= bin/platform-deploy
PLATFORM_DEPLOY_CONFIG ?= deploy/kubernetes/deploy.yaml
PLATFORM_IMAGE_TAG ?=
PLATFORM_SKIP_BUILD ?= false
ARCHIVE_INVENTORY ?= deploy/project-archives.yaml
ARCHIVE_ROOT ?= ../已部署项目归档
NAMESPACE ?=
RELEASE ?=
CONFIRM ?=
RELEASE_VERSION ?= 1.0.0

.PHONY: init local docker-up docker-down frontend-install frontend-build build serve validate plan deploy platform destroy test fmt release-package cicd-docker-up cicd-docker-down cicd-build cicd-serve platform-deploy-build platform-render platform-preflight platform-update platform-status platform-rollback archive-sync archive-plan archive-apply

init:
	go run ./cmd/ops-deploy init --config $(CONFIG)

local: docker-up frontend-install build
	./$(BINARY) serve --config $(CONFIG)

docker-up:
	docker compose up -d --wait mysql redis

docker-down:
	docker compose down

frontend-install:
	npm --prefix frontend ci

frontend-build:
	npm --prefix frontend run build

build: frontend-build
	go build -o $(BINARY) ./cmd/ops-deploy

serve:
	go run ./cmd/ops-deploy serve --config $(CONFIG)

cicd-docker-up:
	docker compose -f compose.cicd.yaml up -d --wait

cicd-docker-down:
	docker compose -f compose.cicd.yaml down

cicd-build: frontend-build
	go build -o bin/ops-deploy-cicd ./cmd/ops-deploy

cicd-serve:
	./bin/ops-deploy-cicd serve --config config.cicd.yaml

validate:
	go run ./cmd/ops-deploy validate --config $(CONFIG) --project $(PROJECT) --environment $(ENVIRONMENT)

plan:
	go run ./cmd/ops-deploy plan --config $(CONFIG) --project $(PROJECT) --environment $(ENVIRONMENT)

deploy:
	go run ./cmd/ops-deploy deploy --config $(CONFIG) --project $(PROJECT) --environment $(ENVIRONMENT)

platform:
	go run ./cmd/ops-deploy platform --config $(CONFIG) --project $(PROJECT) --environment $(ENVIRONMENT)

destroy:
	go run ./cmd/ops-deploy destroy --config $(CONFIG) --project $(PROJECT) --environment $(ENVIRONMENT)

test:
	npm --prefix frontend run typecheck
	go test -race ./...

fmt:
	gofmt -w cmd internal web
	terraform fmt -recursive terraform

release-package:
	go run ./cmd/release-package --version $(RELEASE_VERSION)

# 平台自身的 EKS 发布工具：独立 kubeconfig，不改变本机当前 Context。
platform-deploy-build:
	go build -o $(PLATFORM_DEPLOY_BINARY) ./cmd/platform-deploy

platform-render: platform-deploy-build
	./$(PLATFORM_DEPLOY_BINARY) render --config $(PLATFORM_DEPLOY_CONFIG)

platform-preflight: platform-deploy-build
	./$(PLATFORM_DEPLOY_BINARY) preflight --config $(PLATFORM_DEPLOY_CONFIG)

platform-update: platform-deploy-build
	./$(PLATFORM_DEPLOY_BINARY) deploy --config $(PLATFORM_DEPLOY_CONFIG) $(if $(PLATFORM_IMAGE_TAG),--tag $(PLATFORM_IMAGE_TAG),) $(if $(filter 1 true yes,$(PLATFORM_SKIP_BUILD)),--skip-build,)

platform-status: platform-deploy-build
	./$(PLATFORM_DEPLOY_BINARY) status --config $(PLATFORM_DEPLOY_CONFIG)

platform-rollback: platform-deploy-build
	./$(PLATFORM_DEPLOY_BINARY) rollback --config $(PLATFORM_DEPLOY_CONFIG)

archive-sync:
	go run ./cmd/project-manifest-archive sync --inventory $(ARCHIVE_INVENTORY) --root $(ARCHIVE_ROOT)

archive-plan:
	go run ./cmd/project-manifest-archive plan --root $(ARCHIVE_ROOT) --project $(PROJECT) --environment $(ENVIRONMENT) --namespace $(NAMESPACE) --release $(RELEASE)

archive-apply:
	go run ./cmd/project-manifest-archive apply --root $(ARCHIVE_ROOT) --project $(PROJECT) --environment $(ENVIRONMENT) --namespace $(NAMESPACE) --release $(RELEASE) --confirm $(CONFIRM)
