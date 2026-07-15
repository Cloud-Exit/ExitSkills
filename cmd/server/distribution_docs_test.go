package main

import (
	"os"
	"strings"
	"testing"
)

func TestOpenSourceDistributionArtifacts(t *testing.T) {
	files := map[string][]string{
		"../../Dockerfile": {
			"alpine:latest",
			"golang:1.26.5-alpine@sha256:0178a641fbb4858c5f1b48e34bdaabe0350a330a1b1149aabd498d0699ff5fb2",
			"alpine:latest@sha256:28bd5fe8b56d1bd048e5babf5b10710ebe0bae67db86916198a6eec434943f8b",
			"org.opencontainers.image.source",
			"VOLUME [\"/data\"]",
		},
		"../../LICENSE": {
			"MIT License",
			"Copyright (c) 2026 Cloud Exit B.V.",
			"Permission is hereby granted, free of charge",
		},
		"../../compose.yaml": {
			"services:",
			"exitskills:",
			"postgres:",
			"postgres:18-alpine",
			"postgres:18-alpine@sha256:9a8afca54e7861fd90fab5fdf4c42477a6b1cb7d293595148e674e0a3181de15",
			"GITHUB_CLIENT_ID",
			"GITHUB_CLIENT_SECRET",
			"127.0.0.1:${EXITSKILLS_PORT:-8111}:8080",
			"read_only: true",
			"no-new-privileges:true",
			"POSTGRES_PASSWORD:?set POSTGRES_PASSWORD",
		},
		"../../deploy/helm/exitskills/values.yaml": {
			"repository: ghcr.io/cloud-exit/exitskills",
			"seccompProfile:",
			"type: RuntimeDefault",
		},
		"../../deploy/helm/exitskills/Chart.yaml": {
			"name: exitskills",
			"version: 0.2.0",
			"appVersion: \"0.2.0\"",
		},
		"../../Makefile": {
			"verify:",
			"mod verify",
		},
		"../../.github/workflows/ci.yml": {
			"actions/checkout@9c091bb21b7c1c1d1991bb908d89e4e9dddfe3e0",
			"actions/setup-go@924ae3a1cded613372ab5595356fb5720e22ba16",
			"Azure/setup-helm@9bc31f4ebc9c6b171d7bfbaa5d006ae7abdb4310",
			"persist-credentials: false",
		},
		"../../.github/workflows/release.yml": {
			"docker/setup-buildx-action@bb05f3f5519dd87d3ba754cc423b652a5edd6d2c",
			"docker/login-action@af1e73f918a031802d376d3c8bbc3fe56130a9b0",
			"docker/build-push-action@53b7df96c91f9c12dcc8a07bcb9ccacbed38856a",
			"image=${REGISTRY}/${GITHUB_REPOSITORY,,}",
			"steps.image.outputs.image",
			"push: true",
			"org.opencontainers.image.source=https://github.com/${{ github.repository }}",
			"helm registry login",
			"helm push",
			"dist/exitskills-${{ steps.version.outputs.version }}.tgz",
			"oci://${REGISTRY}/${GITHUB_REPOSITORY_OWNER,,}",
			"packages/container/exitmesh-skills",
		},
		"../../docs/deployment.md": {
			"# Running ExitSkills",
			"docker build",
			"docker compose",
			"helm upgrade --install",
			"oci://ghcr.io/cloud-exit/charts/exitskills",
			"--version 0.2.0",
			"--security-opt no-new-privileges",
			"--cap-drop ALL",
		},
		"../../contribute.md": {
			"# Contributing to ExitSkills",
			"TDD",
			"make test-race",
		},
	}
	for filename, expected := range files {
		contents, err := os.ReadFile(filename)
		if err != nil {
			t.Errorf("read %s: %v", filename, err)
			continue
		}
		for _, value := range expected {
			if !strings.Contains(string(contents), value) {
				t.Errorf("%s is missing %q", filename, value)
			}
		}
	}
}

func TestReleaseUsesSemanticVersions(t *testing.T) {
	contents, err := os.ReadFile("../../.github/workflows/release.yml")
	if err != nil {
		t.Fatal(err)
	}
	workflow := string(contents)
	for _, forbidden := range []string{"GITHUB_RUN_NUMBER", "date -u +%Y%m%d"} {
		if strings.Contains(workflow, forbidden) {
			t.Errorf("release workflow contains date/run versioning %q", forbidden)
		}
	}
	for _, required := range []string{
		"base_version=\"$(awk '$1 == \"version:\" { print $2; exit }' deploy/helm/exitskills/Chart.yaml)\"",
		"^[0-9]+\\.[0-9]+\\.[0-9]+$",
		"tag=v${version}",
		"git tag --list \"v${release_line}.*\"",
	} {
		if !strings.Contains(workflow, required) {
			t.Errorf("release workflow is missing semantic-version logic %q", required)
		}
	}
}

func TestContainerBuildUsesSourcesAlreadyValidatedByReleaseCI(t *testing.T) {
	contents, err := os.ReadFile("../../Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(contents), "go test ./...") {
		t.Fatal("Dockerfile reruns repository tests even though .dockerignore excludes distribution test fixtures")
	}
}
