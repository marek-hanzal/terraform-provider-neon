// Command function-zip wraps an already bundled index.mjs in a deterministic
// ZIP. It is an offline build step, separate from Terraform's resource lifecycle.
package main

import (
	"archive/zip"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

func main() {
	input := flag.String("input", "", "Path to a prebuilt, self-contained Node.js 24 ESM bundle")
	output := flag.String("output", "", "Output ZIP path")
	flag.Parse()
	if *input == "" || *output == "" {
		flag.Usage()
		os.Exit(2)
	}
	if err := packageFunction(*input, *output); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func packageFunction(input, output string) error {
	in, err := os.Open(input)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	if err := os.MkdirAll(filepath.Dir(output), 0755); err != nil {
		return err
	}
	out, err := os.CreateTemp(filepath.Dir(output), ".function-*.zip")
	if err != nil {
		return err
	}
	tempName := out.Name()
	defer func() { _ = out.Close(); _ = os.Remove(tempName) }()
	w := zip.NewWriter(out)
	// Store avoids compressor-version differences. DOS timestamp 1980-01-01,
	// fixed filename and permissions make identical bundles byte-identical.
	h := &zip.FileHeader{Name: "index.mjs", Method: zip.Store, ModifiedDate: 33}
	h.SetMode(0644)
	entry, err := w.CreateHeader(h)
	if err != nil {
		return err
	}
	if _, err := io.Copy(entry, in); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Rename(tempName, output)
}
