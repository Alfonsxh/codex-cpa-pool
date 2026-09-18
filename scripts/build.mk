SHELL := /bin/sh

# Explicit test/build/release/deployment entry. Invoke from the repository root:
#   make -f scripts/build.mk verify
ROOT_DIR := $(abspath $(dir $(lastword $(MAKEFILE_LIST)))/..)

VERSION ?=
PLATFORM ?= linux/amd64
IMAGE_PREFIXES ?=
HARBOR_PREFIX ?=
DOCKERHUB_PREFIX ?=
GHCR_PREFIX ?=
IMAGE_PREFIX ?= ghcr.io/alfonsxh
RELEASE_ARCHIVE ?= $(ROOT_DIR)/dist/codex-cpa-pool-$(VERSION).tar.gz
GH_REPO ?= Alfonsxh/codex-cpa-pool
GIT_REMOTE ?= origin
RELEASE_BRANCH ?= main
TARGET_ENV ?= target.env
TEST_PROJECT ?= codex-cpa-test
NOTIFY_ACTION ?= send

.PHONY: help verify run-test generate-api check-generated-api \
	test-config test-build test-up test-smoke test-down test-faults \
	target-config target-pull target-verify-images target-ownership-status \
	target-activate target-up-core target-up-writers target-up-notifications \
	target-smoke target-ps target-down lease-rehearsal worker-lease-rehearsal \
	privacy-check package images publish publish-harbor publish-dockerhub \
	publish-ghcr publish-all release-check release-verify release release-notify run

help:
	@printf '%s\n' \
	  'make -f scripts/build.mk verify' \
	  'make -f scripts/build.mk run-test' \
	  'make -f scripts/build.mk generate-api' \
	  'make -f scripts/build.mk check-generated-api' \
	  'make -f scripts/build.mk test-config' \
	  'make -f scripts/build.mk test-build [TEST_PROJECT=codex-cpa-test]' \
	  'make -f scripts/build.mk test-up' \
	  'make -f scripts/build.mk test-smoke' \
	  'make -f scripts/build.mk test-faults' \
	  'make -f scripts/build.mk test-down' \
	  'make -f scripts/build.mk target-config TARGET_ENV=/path/to/target.env' \
	  'make -f scripts/build.mk target-verify-images TARGET_ENV=/path/to/target.env' \
	  'make -f scripts/build.mk target-ownership-status TARGET_ENV=/path/to/target.env' \
	  'make -f scripts/build.mk run TARGET_ENV=/path/to/target.env' \
	  'make -f scripts/build.mk lease-rehearsal' \
	  'make -f scripts/build.mk worker-lease-rehearsal' \
	  'make -f scripts/build.mk privacy-check' \
	  'make -f scripts/build.mk package VERSION=v1.0.0' \
	  'make -f scripts/build.mk images VERSION=v1.0.0 [PLATFORM=linux/amd64]' \
	  'make -f scripts/build.mk publish VERSION=v1.0.0 IMAGE_PREFIXES="registry.example.com/team docker.io/user"' \
	  'make -f scripts/build.mk release-verify' \
	  'make -f scripts/build.mk release-check [VERSION=v1.1.0]' \
	  'make -f scripts/build.mk release VERSION=v1.1.0' \
	  'make -f scripts/build.mk release-notify VERSION=v1.1.0 [NOTIFY_ACTION=preview|status|send|edit]'

verify:
	cd "$(ROOT_DIR)" && sh scripts/verify.sh

run-test:
	cd "$(ROOT_DIR)" && sh scripts/test-run.sh

generate-api:
	cd "$(ROOT_DIR)" && sh scripts/generate-api.sh

check-generated-api:
	cd "$(ROOT_DIR)" && sh scripts/check-generated-api.sh

test-config:
	cd "$(ROOT_DIR)" && docker compose -p "$(TEST_PROJECT)" -f docker-compose.test.yml config --quiet

test-build: test-config
	cd "$(ROOT_DIR)" && docker compose -p "$(TEST_PROJECT)" -f docker-compose.test.yml build

test-up: test-config
	cd "$(ROOT_DIR)" && docker compose -p "$(TEST_PROJECT)" -f docker-compose.test.yml up -d --wait

test-smoke:
	cd "$(ROOT_DIR)" && TEST_PROJECT="$(TEST_PROJECT)" sh scripts/test-smoke.sh

test-down:
	cd "$(ROOT_DIR)" && docker compose -p "$(TEST_PROJECT)" -f docker-compose.test.yml down --remove-orphans

test-faults:
	cd "$(ROOT_DIR)" && sh scripts/test-faults.sh

target-config:
	CPA_RELEASE_ROOT="$(ROOT_DIR)" CPA_ENV_FILE="$(TARGET_ENV)" sh "$(ROOT_DIR)/scripts/run.sh" __target config

target-pull:
	CPA_RELEASE_ROOT="$(ROOT_DIR)" CPA_ENV_FILE="$(TARGET_ENV)" sh "$(ROOT_DIR)/scripts/run.sh" __target pull

target-verify-images:
	CPA_RELEASE_ROOT="$(ROOT_DIR)" CPA_ENV_FILE="$(TARGET_ENV)" sh "$(ROOT_DIR)/scripts/run.sh" __target verify-images

target-ownership-status:
	CPA_RELEASE_ROOT="$(ROOT_DIR)" CPA_ENV_FILE="$(TARGET_ENV)" sh "$(ROOT_DIR)/scripts/run.sh" __target ownership-status

target-activate:
	CPA_RELEASE_ROOT="$(ROOT_DIR)" CPA_ENV_FILE="$(TARGET_ENV)" sh "$(ROOT_DIR)/scripts/run.sh" __target activate

target-up-core:
	CPA_RELEASE_ROOT="$(ROOT_DIR)" CPA_ENV_FILE="$(TARGET_ENV)" sh "$(ROOT_DIR)/scripts/run.sh" __target up-core

target-up-writers:
	CPA_RELEASE_ROOT="$(ROOT_DIR)" CPA_ENV_FILE="$(TARGET_ENV)" sh "$(ROOT_DIR)/scripts/run.sh" __target up-writers

target-up-notifications:
	CPA_RELEASE_ROOT="$(ROOT_DIR)" CPA_ENV_FILE="$(TARGET_ENV)" sh "$(ROOT_DIR)/scripts/run.sh" __target up-notifications

target-smoke:
	CPA_RELEASE_ROOT="$(ROOT_DIR)" CPA_ENV_FILE="$(TARGET_ENV)" sh "$(ROOT_DIR)/scripts/run.sh" __target smoke

target-ps:
	CPA_RELEASE_ROOT="$(ROOT_DIR)" CPA_ENV_FILE="$(TARGET_ENV)" sh "$(ROOT_DIR)/scripts/run.sh" __target ps

target-down:
	CPA_RELEASE_ROOT="$(ROOT_DIR)" CPA_ENV_FILE="$(TARGET_ENV)" sh "$(ROOT_DIR)/scripts/run.sh" __target down

lease-rehearsal:
	cd "$(ROOT_DIR)" && go test -count=1 -run '^TestWriterLeaseGenerationTransferFencesStaleOwner$$' ./internal/ownership

worker-lease-rehearsal:
	cd "$(ROOT_DIR)" && go test -count=1 -run '^TestGoWorkerLeaseGroupTransfersAllScopesAndRejectsDuplicate$$' ./internal/ownership

privacy-check:
	cd "$(ROOT_DIR)" && go run ./cmd/releasectl privacy --root .

package: privacy-check
	@test -n "$(VERSION)" || { echo 'VERSION 不能为空，例如 VERSION=v1.0.0' >&2; exit 1; }
	cd "$(ROOT_DIR)" && sh scripts/package-release.sh "$(RELEASE_ARCHIVE)"

images: privacy-check
	@test -n "$(VERSION)" || { echo 'VERSION 不能为空，例如 VERSION=v1.0.0' >&2; exit 1; }
	cd "$(ROOT_DIR)" && VERSION="$(VERSION)" PLATFORM="$(PLATFORM)" sh scripts/release-images.sh build

publish: privacy-check
	@test -n "$(VERSION)" || { echo 'VERSION 不能为空，例如 VERSION=v1.0.0' >&2; exit 1; }
	@test -n "$(IMAGE_PREFIXES)" || { echo 'IMAGE_PREFIXES 不能为空' >&2; exit 1; }
	cd "$(ROOT_DIR)" && VERSION="$(VERSION)" PLATFORM="$(PLATFORM)" IMAGE_PREFIXES="$(IMAGE_PREFIXES)" sh scripts/release-images.sh publish

publish-harbor:
	@test -n "$(HARBOR_PREFIX)" || { echo 'HARBOR_PREFIX 不能为空' >&2; exit 1; }
	$(MAKE) -f "$(ROOT_DIR)/scripts/build.mk" publish VERSION="$(VERSION)" PLATFORM="$(PLATFORM)" IMAGE_PREFIXES="$(HARBOR_PREFIX)"

publish-dockerhub:
	@test -n "$(DOCKERHUB_PREFIX)" || { echo 'DOCKERHUB_PREFIX 不能为空' >&2; exit 1; }
	$(MAKE) -f "$(ROOT_DIR)/scripts/build.mk" publish VERSION="$(VERSION)" PLATFORM="$(PLATFORM)" IMAGE_PREFIXES="$(DOCKERHUB_PREFIX)"

publish-ghcr:
	@test -n "$(GHCR_PREFIX)" || { echo 'GHCR_PREFIX 不能为空' >&2; exit 1; }
	$(MAKE) -f "$(ROOT_DIR)/scripts/build.mk" publish VERSION="$(VERSION)" PLATFORM="$(PLATFORM)" IMAGE_PREFIXES="$(GHCR_PREFIX)"

publish-all:
	@test -n "$(HARBOR_PREFIX)" || { echo 'HARBOR_PREFIX 不能为空' >&2; exit 1; }
	@test -n "$(DOCKERHUB_PREFIX)" || { echo 'DOCKERHUB_PREFIX 不能为空' >&2; exit 1; }
	$(MAKE) -f "$(ROOT_DIR)/scripts/build.mk" publish VERSION="$(VERSION)" PLATFORM="$(PLATFORM)" IMAGE_PREFIXES="$(HARBOR_PREFIX) $(DOCKERHUB_PREFIX)"

release-verify:
	cd "$(ROOT_DIR)" && PLATFORM="$(PLATFORM)" sh scripts/local-release.sh verify

release-check:
	cd "$(ROOT_DIR)" && VERSION="$(VERSION)" IMAGE_PREFIX="$(IMAGE_PREFIX)" GH_REPO="$(GH_REPO)" GIT_REMOTE="$(GIT_REMOTE)" RELEASE_BRANCH="$(RELEASE_BRANCH)" sh scripts/github-release.sh check

release:
	cd "$(ROOT_DIR)" && VERSION="$(VERSION)" IMAGE_PREFIX="$(IMAGE_PREFIX)" GH_REPO="$(GH_REPO)" GIT_REMOTE="$(GIT_REMOTE)" RELEASE_BRANCH="$(RELEASE_BRANCH)" sh scripts/github-release.sh publish

release-notify:
	cd "$(ROOT_DIR)" && NOTIFY_ACTION="$(NOTIFY_ACTION)" VERSION="$(VERSION)" IMAGE_PREFIX="$(IMAGE_PREFIX)" GH_REPO="$(GH_REPO)" GIT_REMOTE="$(GIT_REMOTE)" RELEASE_BRANCH="$(RELEASE_BRANCH)" sh scripts/github-release.sh notify

run:
	$(MAKE) -f "$(ROOT_DIR)/scripts/build.mk" target-config TARGET_ENV="$(TARGET_ENV)"
	$(MAKE) -f "$(ROOT_DIR)/scripts/build.mk" target-pull TARGET_ENV="$(TARGET_ENV)"
	$(MAKE) -f "$(ROOT_DIR)/scripts/build.mk" target-verify-images TARGET_ENV="$(TARGET_ENV)"
	$(MAKE) -f "$(ROOT_DIR)/scripts/build.mk" target-activate TARGET_ENV="$(TARGET_ENV)"
	$(MAKE) -f "$(ROOT_DIR)/scripts/build.mk" target-up-core TARGET_ENV="$(TARGET_ENV)"
	$(MAKE) -f "$(ROOT_DIR)/scripts/build.mk" target-up-writers TARGET_ENV="$(TARGET_ENV)"
	$(MAKE) -f "$(ROOT_DIR)/scripts/build.mk" target-smoke TARGET_ENV="$(TARGET_ENV)"
