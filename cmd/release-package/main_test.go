package main

import (
	"archive/zip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVersionPattern(t *testing.T) {
	for _, value := range []string{"1.0.0", "1.2.3-rc.1", "1.2.3+build.4"} {
		if !versionPattern.MatchString(value) {
			t.Fatalf("expected %q to be accepted", value)
		}
	}
	for _, value := range []string{"v1.0.0", "1.0", "1.0.0 bad", ""} {
		if versionPattern.MatchString(value) {
			t.Fatalf("expected %q to be rejected", value)
		}
	}
}

func TestCopyPathExcludesRuntimeAndTerraformState(t *testing.T) {
	source := t.TempDir()
	destination := filepath.Join(t.TempDir(), "copied")
	files := map[string]string{
		"main.tf":                       "resource {}",
		"nested/readme.txt":             "safe",
		"nested/runtime.tfstate":        "secret",
		"nested/runtime.tfstate.backup": "secret",
		"nested/.terraform/plugin":      "binary",
	}
	for name, content := range files {
		path := filepath.Join(source, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := copyPath(source, destination); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"main.tf", "nested/readme.txt"} {
		if _, err := os.Stat(filepath.Join(destination, name)); err != nil {
			t.Fatalf("expected %s in release copy: %v", name, err)
		}
	}
	for _, name := range []string{"nested/runtime.tfstate", "nested/runtime.tfstate.backup", "nested/.terraform/plugin"} {
		if _, err := os.Stat(filepath.Join(destination, name)); !os.IsNotExist(err) {
			t.Fatalf("sensitive/runtime path %s was copied", name)
		}
	}
}

func TestCreateZipUsesSingleReleaseRoot(t *testing.T) {
	stage := filepath.Join(t.TempDir(), "stage")
	if err := os.MkdirAll(filepath.Join(stage, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stage, "docs", "readme.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "release.zip")
	if err := createZip(destination, stage, "awsinfra_1.0.0_windows_amd64"); err != nil {
		t.Fatal(err)
	}
	archive, err := zip.OpenReader(destination)
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	found := false
	for _, entry := range archive.File {
		if strings.TrimSuffix(entry.Name, "/") == "awsinfra_1.0.0_windows_amd64/docs/readme.txt" {
			found = true
		}
	}
	if !found {
		t.Fatal("archive did not contain the expected versioned root")
	}
}
