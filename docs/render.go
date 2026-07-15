package apidocs

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sync"
)

const DefaultRedocVersion = "v2.5.3"

//go:embed openapi.json
var OpenAPI []byte

var (
	htmlOnce sync.Once
	html     []byte
	htmlErr  error
)

func HTML() ([]byte, error) {
	htmlOnce.Do(func() {
		var output bytes.Buffer
		htmlErr = Render(&output, OpenAPI, DefaultRedocVersion)
		html = output.Bytes()
	})
	return html, htmlErr
}

func Render(writer io.Writer, rawSpec []byte, redocVersion string) error {
	if !regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+$`).MatchString(redocVersion) {
		return fmt.Errorf("invalid Redoc version %q", redocVersion)
	}
	decoder := json.NewDecoder(bytes.NewReader(rawSpec))
	decoder.UseNumber()
	var spec map[string]any
	if err := decoder.Decode(&spec); err != nil {
		return fmt.Errorf("decode OpenAPI JSON: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("OpenAPI input must contain exactly one JSON document")
	}
	if spec["openapi"] == nil || spec["info"] == nil || spec["paths"] == nil {
		return errors.New("OpenAPI input must contain openapi, info, and paths")
	}
	embedded, err := json.Marshal(spec)
	if err != nil {
		return fmt.Errorf("encode embedded OpenAPI JSON: %w", err)
	}
	_, err = fmt.Fprintf(writer, `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <meta name="description" content="ExitSkills API reference">
  <title>ExitSkills API</title>
  <style>body{margin:0;padding:0}noscript{display:block;padding:2rem;font-family:sans-serif}</style>
</head>
<body>
  <noscript>JavaScript is required to render the ExitSkills API reference.</noscript>
  <div id="redoc-container"></div>
  <script src="https://cdn.redoc.ly/redoc/%s/bundles/redoc.standalone.js"></script>
  <script>
    const spec = %s;
    Redoc.init(spec, {
      hideDownloadButton: false,
      nativeScrollbars: true,
      requiredPropsFirst: true,
      sortPropsAlphabetically: true,
      theme: { colors: { primary: { main: '#e64a2e' } }, typography: { fontFamily: 'Inter, system-ui, sans-serif' } }
    }, document.getElementById('redoc-container'));
  </script>
</body>
</html>
`, redocVersion, embedded)
	return err
}
