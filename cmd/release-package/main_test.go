package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPackageChartIsDeterministic(t *testing.T) {
	t.Parallel()

	chartDir := writeTestChart(t)
	firstDir := t.TempDir()
	secondDir := t.TempDir()
	sourceDate := time.Unix(1_700_000_000, 0).UTC()

	first, err := packageChart(chartDir, firstDir, sourceDate)
	if err != nil {
		t.Fatal(err)
	}
	second, err := packageChart(chartDir, secondDir, sourceDate)
	if err != nil {
		t.Fatal(err)
	}

	firstBytes, err := os.ReadFile(first)
	if err != nil {
		t.Fatal(err)
	}
	secondBytes, err := os.ReadFile(second)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(firstBytes, secondBytes) {
		t.Fatal("identical chart inputs produced different archives")
	}

	entries := readArchive(t, first)
	if entries["example/Chart.yaml"] != "name: example\nversion: 1.2.3\n" {
		t.Fatalf("Chart.yaml missing from archive: %#v", entries)
	}
	if entries["example/templates/deployment.yaml"] != "kind: Deployment\n" {
		t.Fatalf("template missing from archive: %#v", entries)
	}
}

func TestPackageChartRejectsSymlinks(t *testing.T) {
	t.Parallel()

	chartDir := writeTestChart(t)
	if err := os.Symlink("Chart.yaml", filepath.Join(chartDir, "linked-chart")); err != nil {
		t.Fatal(err)
	}
	if _, err := packageChart(chartDir, t.TempDir(), time.Unix(1_700_000_000, 0)); err == nil {
		t.Fatal("expected symlink to be rejected")
	}
}

func TestPackageChartRejectsHelmIgnore(t *testing.T) {
	t.Parallel()

	chartDir := writeTestChart(t)
	if err := os.WriteFile(filepath.Join(chartDir, ".helmignore"), []byte("ignored\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := packageChart(chartDir, t.TempDir(), time.Unix(1_700_000_000, 0)); err == nil {
		t.Fatal("expected .helmignore to be rejected")
	}
}

func writeTestChart(t *testing.T) string {
	t.Helper()
	chartDir := t.TempDir()
	if err := os.Mkdir(filepath.Join(chartDir, "templates"), 0o755); err != nil {
		t.Fatal(err)
	}
	for path, contents := range map[string]string{
		"Chart.yaml":                "name: example\nversion: 1.2.3\n",
		"templates/deployment.yaml": "kind: Deployment\n",
	} {
		if err := os.WriteFile(filepath.Join(chartDir, path), []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return chartDir
}

func readArchive(t *testing.T, path string) map[string]string {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	gzipReader, err := gzip.NewReader(file)
	if err != nil {
		t.Fatal(err)
	}
	defer gzipReader.Close()

	entries := make(map[string]string)
	tarReader := tar.NewReader(gzipReader)
	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		contents, err := io.ReadAll(tarReader)
		if err != nil {
			t.Fatal(err)
		}
		entries[header.Name] = string(contents)
	}
	return entries
}
