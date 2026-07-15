package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	apidocs "github.com/exitmesh/skills/docs"
)

const defaultRedocVersion = apidocs.DefaultRedocVersion

func main() {
	specPath := flag.String("spec", "docs/openapi.json", "path to the OpenAPI JSON document")
	outputPath := flag.String("out", "dist/api-docs/index.html", "generated Redoc HTML path")
	redocVersion := flag.String("redoc-version", defaultRedocVersion, "pinned Redoc release")
	flag.Parse()

	spec, err := os.ReadFile(*specPath)
	if err != nil {
		fatal(err)
	}
	var output bytes.Buffer
	if err := generate(&output, spec, *redocVersion); err != nil {
		fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(*outputPath), 0o755); err != nil {
		fatal(err)
	}
	if err := os.WriteFile(*outputPath, output.Bytes(), 0o644); err != nil {
		fatal(err)
	}
	fmt.Printf("generated %s from %s with Redoc %s\n", *outputPath, *specPath, *redocVersion)
}

func generate(writer io.Writer, rawSpec []byte, redocVersion string) error {
	return apidocs.Render(writer, rawSpec, redocVersion)
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "generate-api-docs:", err)
	os.Exit(1)
}
