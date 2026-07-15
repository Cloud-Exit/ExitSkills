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
		"../../deploy/helm/exitmesh-skills/values.yaml": {
			"seccompProfile:",
			"type: RuntimeDefault",
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
		"../../docs/deployment.md": {
			"# Running ExitSkills",
			"docker build",
			"docker compose",
			"helm upgrade --install",
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
