package main

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"sigs.k8s.io/yaml"
)

type chartMetadata struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

func main() {
	chartDir := flag.String("chart", "", "chart source directory")
	destination := flag.String("destination", "", "package output directory")
	sourceDate := flag.Int64("source-date", 0, "Unix timestamp used for every archive entry")
	flag.Parse()

	if *chartDir == "" || *destination == "" || *sourceDate <= 0 {
		fmt.Fprintln(os.Stderr, "--chart, --destination, and a positive --source-date are required")
		os.Exit(2)
	}

	output, err := packageChart(*chartDir, *destination, time.Unix(*sourceDate, 0).UTC())
	if err != nil {
		fmt.Fprintf(os.Stderr, "package chart: %v\n", err)
		os.Exit(1)
	}
	fmt.Println(output)
}

func packageChart(chartDir, destination string, sourceDate time.Time) (string, error) {
	metadataBytes, err := os.ReadFile(filepath.Join(chartDir, "Chart.yaml"))
	if err != nil {
		return "", fmt.Errorf("read Chart.yaml: %w", err)
	}

	var metadata chartMetadata
	if err := yaml.Unmarshal(metadataBytes, &metadata); err != nil {
		return "", fmt.Errorf("parse Chart.yaml: %w", err)
	}
	if metadata.Name == "" || metadata.Version == "" {
		return "", errors.New("Chart.yaml must contain name and version")
	}
	if strings.ContainsAny(metadata.Name+metadata.Version, `/\\`) {
		return "", errors.New("chart name and version must not contain path separators")
	}

	if err := os.MkdirAll(destination, 0o755); err != nil {
		return "", fmt.Errorf("create destination: %w", err)
	}
	output := filepath.Join(destination, fmt.Sprintf("%s-%s.tgz", metadata.Name, metadata.Version))
	file, err := os.Create(output)
	if err != nil {
		return "", fmt.Errorf("create archive: %w", err)
	}

	succeeded := false
	defer func() {
		_ = file.Close()
		if !succeeded {
			_ = os.Remove(output)
		}
	}()

	gzipWriter, err := gzip.NewWriterLevel(file, gzip.BestCompression)
	if err != nil {
		return "", fmt.Errorf("create gzip writer: %w", err)
	}
	gzipWriter.Header.ModTime = sourceDate
	gzipWriter.Header.OS = 255
	tarWriter := tar.NewWriter(gzipWriter)

	err = filepath.WalkDir(chartDir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == chartDir {
			return nil
		}

		relative, err := filepath.Rel(chartDir, path)
		if err != nil {
			return err
		}
		if entry.Name() == ".helmignore" {
			return errors.New(".helmignore is not supported by the deterministic packager")
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("symbolic links are not allowed: %s", relative)
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("unsupported chart entry: %s", relative)
		}

		info, err := entry.Info()
		if err != nil {
			return err
		}
		header := &tar.Header{
			Name:       filepath.ToSlash(filepath.Join(metadata.Name, relative)),
			Mode:       int64(info.Mode().Perm()),
			Size:       info.Size(),
			ModTime:    sourceDate,
			AccessTime: sourceDate,
			ChangeTime: sourceDate,
			Typeflag:   tar.TypeReg,
			Format:     tar.FormatPAX,
		}
		if err := tarWriter.WriteHeader(header); err != nil {
			return err
		}

		input, err := os.Open(path)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(tarWriter, input)
		closeErr := input.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	})
	if err != nil {
		_ = tarWriter.Close()
		_ = gzipWriter.Close()
		return "", err
	}
	if err := tarWriter.Close(); err != nil {
		_ = gzipWriter.Close()
		return "", err
	}
	if err := gzipWriter.Close(); err != nil {
		return "", err
	}
	if err := file.Close(); err != nil {
		return "", err
	}

	succeeded = true
	return output, nil
}
