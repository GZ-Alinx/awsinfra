package main

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

var versionPattern = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(?:[-+][0-9A-Za-z.-]+)?$`)

type target struct {
	OS   string
	Arch string
}

var releaseTargets = []target{
	{OS: "windows", Arch: "amd64"},
	{OS: "darwin", Arch: "amd64"},
	{OS: "darwin", Arch: "arm64"},
	{OS: "linux", Arch: "amd64"},
	{OS: "linux", Arch: "arm64"},
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "release package error:", err)
		os.Exit(1)
	}
}

func run() error {
	flags := flag.NewFlagSet("release-package", flag.ContinueOnError)
	version := flags.String("version", "", "semantic version without the v prefix, for example 1.0.0")
	output := flags.String("output", "dist/release", "release artifact directory")
	skipFrontend := flags.Bool("skip-frontend", false, "reuse the existing web/dist frontend build")
	if err := flags.Parse(os.Args[1:]); err != nil {
		return err
	}
	if !versionPattern.MatchString(strings.TrimSpace(*version)) {
		return errors.New("--version must be a semantic version such as 1.0.0")
	}
	root, err := findRepositoryRoot()
	if err != nil {
		return err
	}
	if !*skipFrontend {
		if err := runCommand(root, nil, "npm", "--prefix", "frontend", "ci"); err != nil {
			return err
		}
		if err := runCommand(root, nil, "npm", "--prefix", "frontend", "run", "build"); err != nil {
			return err
		}
	}
	if _, err := os.Stat(filepath.Join(root, "web", "dist", "index.html")); err != nil {
		return fmt.Errorf("frontend build is unavailable: %w", err)
	}

	outputDir := *output
	if !filepath.IsAbs(outputDir) {
		outputDir = filepath.Join(root, outputDir)
	}
	if err := os.RemoveAll(outputDir); err != nil {
		return err
	}
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return err
	}
	temporary, err := os.MkdirTemp("", "awsinfra-release-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(temporary)

	artifacts := make([]string, 0, len(releaseTargets))
	for _, item := range releaseTargets {
		artifact, err := buildTarget(root, temporary, outputDir, strings.TrimSpace(*version), item)
		if err != nil {
			return fmt.Errorf("build %s/%s: %w", item.OS, item.Arch, err)
		}
		artifacts = append(artifacts, artifact)
		fmt.Println("created", artifact)
	}
	if err := writeChecksums(outputDir, artifacts); err != nil {
		return err
	}
	fmt.Println("created", filepath.Join(outputDir, "checksums.txt"))
	return nil
}

func findRepositoryRoot() (string, error) {
	current, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(current, "go.mod")); err == nil {
			if _, err := os.Stat(filepath.Join(current, "frontend", "package.json")); err == nil {
				return current, nil
			}
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", errors.New("run release-package inside the AWSInfra repository")
		}
		current = parent
	}
}

func buildTarget(root, temporary, outputDir, version string, item target) (string, error) {
	baseName := fmt.Sprintf("awsinfra_%s_%s_%s", version, item.OS, item.Arch)
	stage := filepath.Join(temporary, baseName)
	if err := os.MkdirAll(stage, 0o755); err != nil {
		return "", err
	}
	executable := "awsinfra"
	if item.OS == "windows" {
		executable += ".exe"
	}
	buildEnvironment := []string{"CGO_ENABLED=0", "GOOS=" + item.OS, "GOARCH=" + item.Arch}
	linkerFlags := "-s -w -X github.com/GZ-Alinx/awsinfra/internal/httpapi.Version=" + version
	if err := runCommand(root, buildEnvironment, "go", "build", "-trimpath", "-ldflags", linkerFlags, "-o", filepath.Join(stage, executable), "./cmd/ops-deploy"); err != nil {
		return "", err
	}

	files := []string{"config.yaml", "compose.yaml", "README.md", "RELEASE_NOTES.md", "LICENSE", "NOTICE"}
	for _, name := range files {
		if err := copyPath(filepath.Join(root, name), filepath.Join(stage, name)); err != nil {
			return "", err
		}
	}
	for _, name := range []string{"docs", "terraform", filepath.Join("deploy", "aws")} {
		if err := copyPath(filepath.Join(root, name), filepath.Join(stage, name)); err != nil {
			return "", err
		}
	}
	for _, name := range []string{"data", "environments"} {
		if err := os.MkdirAll(filepath.Join(stage, name), 0o700); err != nil {
			return "", err
		}
	}
	if err := os.WriteFile(filepath.Join(stage, "VERSION"), []byte(version+"\n"), 0o644); err != nil {
		return "", err
	}

	if item.OS == "windows" {
		artifact := filepath.Join(outputDir, baseName+".zip")
		return artifact, createZip(artifact, stage, baseName)
	}
	artifact := filepath.Join(outputDir, baseName+".tar.gz")
	return artifact, createTarGzip(artifact, stage, baseName)
}

func runCommand(directory string, extraEnvironment []string, name string, arguments ...string) error {
	command := exec.Command(name, arguments...) // #nosec G204 -- the release command and target matrix are compile-time constants.
	command.Dir = directory
	command.Env = append(os.Environ(), extraEnvironment...)
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	if err := command.Run(); err != nil {
		return fmt.Errorf("%s failed: %w", name, err)
	}
	return nil
}

func copyPath(source, destination string) error {
	info, err := os.Stat(source)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return copyFile(source, destination, info.Mode())
	}
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		if ignoredReleasePath(relative, entry.IsDir()) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		targetPath := filepath.Join(destination, relative)
		if entry.IsDir() {
			return os.MkdirAll(targetPath, 0o755)
		}
		fileInfo, err := entry.Info()
		if err != nil {
			return err
		}
		return copyFile(path, targetPath, fileInfo.Mode())
	})
}

func ignoredReleasePath(relative string, directory bool) bool {
	base := filepath.Base(relative)
	if directory && (base == ".terraform" || base == "node_modules" || base == ".git") {
		return true
	}
	return base == ".DS_Store" || strings.Contains(base, ".tfstate") || base == "crash.log"
}

func copyFile(source, destination string, mode fs.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return err
	}
	input, err := os.Open(filepath.Clean(source))
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode.Perm()) // #nosec G304 -- paths are rooted in the repository and temporary release directory.
	if err != nil {
		return err
	}
	if _, err := io.Copy(output, input); err != nil {
		_ = output.Close()
		return err
	}
	return output.Close()
}

func archiveEntries(root string) ([]string, error) {
	var paths []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path != root {
			paths = append(paths, path)
		}
		return nil
	})
	sort.Strings(paths)
	return paths, err
}

func createZip(destination, stage, rootName string) error {
	file, err := os.Create(destination) // #nosec G304 -- destination is controlled by the release output flag.
	if err != nil {
		return err
	}
	archive := zip.NewWriter(file)
	paths, err := archiveEntries(stage)
	if err == nil {
		for _, path := range paths {
			info, statErr := os.Stat(path)
			if statErr != nil {
				err = statErr
				break
			}
			relative, _ := filepath.Rel(stage, path)
			name := filepath.ToSlash(filepath.Join(rootName, relative))
			if info.IsDir() {
				name += "/"
			}
			header, headerErr := zip.FileInfoHeader(info)
			if headerErr != nil {
				err = headerErr
				break
			}
			header.Name = name
			header.Method = zip.Deflate
			header.Modified = time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC)
			writer, createErr := archive.CreateHeader(header)
			if createErr != nil {
				err = createErr
				break
			}
			if !info.IsDir() {
				input, openErr := os.Open(filepath.Clean(path))
				if openErr != nil {
					err = openErr
					break
				}
				_, copyErr := io.Copy(writer, input)
				closeErr := input.Close()
				if copyErr != nil {
					err = copyErr
					break
				}
				if closeErr != nil {
					err = closeErr
					break
				}
			}
		}
	}
	err = errors.Join(err, archive.Close(), file.Close())
	if err != nil {
		_ = os.Remove(destination)
	}
	return err
}

func createTarGzip(destination, stage, rootName string) error {
	file, err := os.Create(destination) // #nosec G304 -- destination is controlled by the release output flag.
	if err != nil {
		return err
	}
	compressed := gzip.NewWriter(file)
	compressed.Name = ""
	compressed.ModTime = time.Unix(0, 0)
	archive := tar.NewWriter(compressed)
	paths, err := archiveEntries(stage)
	if err == nil {
		for _, path := range paths {
			info, statErr := os.Stat(path)
			if statErr != nil {
				err = statErr
				break
			}
			relative, _ := filepath.Rel(stage, path)
			header, headerErr := tar.FileInfoHeader(info, "")
			if headerErr != nil {
				err = headerErr
				break
			}
			header.Name = filepath.ToSlash(filepath.Join(rootName, relative))
			header.ModTime = time.Unix(0, 0)
			header.AccessTime = time.Time{}
			header.ChangeTime = time.Time{}
			if writeErr := archive.WriteHeader(header); writeErr != nil {
				err = writeErr
				break
			}
			if !info.IsDir() {
				input, openErr := os.Open(filepath.Clean(path))
				if openErr != nil {
					err = openErr
					break
				}
				_, copyErr := io.Copy(archive, input)
				closeErr := input.Close()
				if copyErr != nil {
					err = copyErr
					break
				}
				if closeErr != nil {
					err = closeErr
					break
				}
			}
		}
	}
	err = errors.Join(err, archive.Close(), compressed.Close(), file.Close())
	if err != nil {
		_ = os.Remove(destination)
	}
	return err
}

func writeChecksums(outputDir string, artifacts []string) error {
	sort.Strings(artifacts)
	lines := make([]string, 0, len(artifacts))
	for _, artifact := range artifacts {
		file, err := os.Open(filepath.Clean(artifact))
		if err != nil {
			return err
		}
		digest := sha256.New()
		_, copyErr := io.Copy(digest, file)
		closeErr := file.Close()
		if copyErr != nil || closeErr != nil {
			return errors.Join(copyErr, closeErr)
		}
		lines = append(lines, hex.EncodeToString(digest.Sum(nil))+"  "+filepath.Base(artifact))
	}
	return os.WriteFile(filepath.Join(outputDir, "checksums.txt"), []byte(strings.Join(lines, "\n")+"\n"), 0o644)
}
