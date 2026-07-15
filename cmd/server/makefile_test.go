package main

import (
	"os"
	"strings"
	"testing"
)

func TestMakeRunForcesInitialIndexRefresh(t *testing.T) {
	contents, err := os.ReadFile("../../Makefile")
	if err != nil {
		t.Fatal(err)
	}
	makefile := string(contents)
	start := strings.Index(makefile, "\nrun:\n")
	if start < 0 {
		t.Fatal("Makefile has no run target")
	}
	block := makefile[start+1:]
	if end := strings.Index(block, "\n\n"); end >= 0 {
		block = block[:end]
	}
	if !strings.Contains(block, "INDEX_ON_START=true") {
		t.Fatalf("run target does not force an initial index refresh:\n%s", block)
	}
}
