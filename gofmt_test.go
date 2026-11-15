package main

import (
	"go/format"
	"os"
	"testing"
)

// Formatting is the gate's to settle, as it is in the other modules.
func TestSourceIsGofmtFormatted(t *testing.T) {
	for _, path := range goFilesUnder(t, ".") {
		src, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("read %s: %v", path, err)
			continue
		}
		want, err := format.Source(src)
		if err != nil {
			t.Errorf("%s does not parse: %v", path, err)
			continue
		}
		if string(want) != string(src) {
			t.Errorf("%s is not gofmt-formatted — run `gofmt -w %s`", path, path)
		}
	}
}
