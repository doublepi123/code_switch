package main

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

func cmdUpgrade(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("upgrade", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	repo := fs.String("repo", defaultUpgradeRepo, "GitHub repository in owner/repo form")
	tag := fs.String("tag", "", "release tag to install instead of latest")
	installPath := fs.String("install-path", "", "override target executable path")
	dryRun := fs.Bool("dry-run", false, "print the download URL and target path without installing")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("usage: claude-switch upgrade [--tag vX.Y.Z] [--install-path PATH]")
	}

	target := strings.TrimSpace(*installPath)
	if target == "" {
		exe, err := os.Executable()
		if err != nil {
			return fmt.Errorf("locate current executable: %w", err)
		}
		target = exe
	}

	opts := upgradeOptions{
		repo:        strings.TrimSpace(*repo),
		tag:         strings.TrimSpace(*tag),
		installPath: target,
		baseURL:     "https://github.com",
		client:      &http.Client{Timeout: 5 * time.Minute},
		out:         out,
		dryRun:      *dryRun,
	}
	return performUpgrade(opts)
}

type upgradeOptions struct {
	repo        string
	tag         string
	installPath string
	baseURL     string
	client      *http.Client
	out         io.Writer
	dryRun      bool
}

func performUpgrade(opts upgradeOptions) error {
	if opts.repo == "" {
		opts.repo = defaultUpgradeRepo
	}
	if opts.baseURL == "" {
		opts.baseURL = "https://github.com"
	}
	if opts.client == nil {
		opts.client = &http.Client{Timeout: 5 * time.Minute}
	}
	if opts.out == nil {
		opts.out = io.Discard
	}

	asset, err := upgradeAssetName(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return err
	}
	if opts.installPath == "" {
		return errors.New("missing install path")
	}

	targetTag := strings.TrimSpace(opts.tag)
	if targetTag == "" || targetTag == "latest" {
		targetTag, err = latestReleaseTag(context.Background(), opts.client, opts.baseURL, opts.repo)
		if err != nil {
			return err
		}
	}
	if shouldSkipUpgrade(version, targetTag) {
		fmt.Fprintf(opts.out, "claude-switch is already up to date (%s)\n", version)
		return nil
	}
	if strings.TrimSpace(version) != "" && version != "dev" {
		fmt.Fprintf(opts.out, "current: %s\n", version)
	}
	if targetTag != "" {
		fmt.Fprintf(opts.out, "latest: %s\n", targetTag)
	}

	downloadURL := releaseDownloadURL(opts.baseURL, opts.repo, targetTag, asset)
	fmt.Fprintf(opts.out, "target: %s\n", opts.installPath)
	fmt.Fprintf(opts.out, "download: %s\n", downloadURL)
	if opts.dryRun {
		return nil
	}

	tmpDir, err := os.MkdirTemp("", "claude-switch-upgrade-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpDir)

	archivePath := filepath.Join(tmpDir, asset)
	if err := downloadFile(context.Background(), opts.client, downloadURL, archivePath); err != nil {
		return err
	}

	binaryName := "cs"
	if runtime.GOOS == "windows" {
		binaryName = "cs.exe"
	}
	extractedPath := filepath.Join(tmpDir, binaryName)
	if strings.HasSuffix(asset, ".zip") {
		err = extractZipBinary(archivePath, binaryName, extractedPath)
	} else {
		err = extractTarGzBinary(archivePath, binaryName, extractedPath)
	}
	if err != nil {
		return err
	}

	mode := os.FileMode(0o755)
	if info, err := os.Stat(opts.installPath); err == nil {
		mode = info.Mode().Perm() | 0o111
	}
	if err := os.Chmod(extractedPath, mode); err != nil {
		return err
	}
	if err := replaceExecutable(extractedPath, opts.installPath); err != nil {
		return err
	}

	fmt.Fprintf(opts.out, "upgraded claude-switch to latest release\n")
	return nil
}

func latestReleaseTag(ctx context.Context, client *http.Client, baseURL, repo string) (string, error) {
	latestURL := fmt.Sprintf("%s/%s/releases/latest", strings.TrimRight(strings.TrimSpace(baseURL), "/"), strings.Trim(strings.TrimSpace(repo), "/"))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, latestURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "claude-switch/"+version)

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 400 {
		return "", fmt.Errorf("check latest release failed: %s", resp.Status)
	}
	tag := tagFromReleaseURL(resp.Request.URL)
	if tag == "" {
		return "", fmt.Errorf("could not determine latest release tag from %s", resp.Request.URL.String())
	}
	return tag, nil
}

func tagFromReleaseURL(u *url.URL) string {
	if u == nil {
		return ""
	}
	parts := strings.Split(strings.Trim(u.EscapedPath(), "/"), "/")
	for i := 0; i+1 < len(parts); i++ {
		if parts[i] != "tag" {
			continue
		}
		tag, err := url.PathUnescape(parts[i+1])
		if err != nil {
			return ""
		}
		return tag
	}
	return ""
}

func shouldSkipUpgrade(current, target string) bool {
	current = strings.TrimSpace(current)
	target = strings.TrimSpace(target)
	if current == "" || current == "dev" || target == "" {
		return false
	}
	cmp, ok := compareReleaseVersions(current, target)
	if !ok {
		return current == target
	}
	return cmp >= 0
}

func compareReleaseVersions(current, target string) (int, bool) {
	currentVersion, ok := parseReleaseVersion(current)
	if !ok {
		return 0, false
	}
	targetVersion, ok := parseReleaseVersion(target)
	if !ok {
		return 0, false
	}
	for i := 0; i < len(currentVersion.numbers) || i < len(targetVersion.numbers); i++ {
		left := 0
		if i < len(currentVersion.numbers) {
			left = currentVersion.numbers[i]
		}
		right := 0
		if i < len(targetVersion.numbers) {
			right = targetVersion.numbers[i]
		}
		switch {
		case left > right:
			return 1, true
		case left < right:
			return -1, true
		}
	}
	switch {
	case currentVersion.preRelease == "" && targetVersion.preRelease != "":
		return 1, true
	case currentVersion.preRelease != "" && targetVersion.preRelease == "":
		return -1, true
	case currentVersion.preRelease > targetVersion.preRelease:
		return 1, true
	case currentVersion.preRelease < targetVersion.preRelease:
		return -1, true
	default:
		return 0, true
	}
}

type releaseVersion struct {
	numbers    []int
	preRelease string
}

func parseReleaseVersion(value string) (releaseVersion, bool) {
	value = strings.TrimSpace(value)
	value = strings.TrimPrefix(value, "v")
	value = strings.TrimPrefix(value, "V")
	if value == "" {
		return releaseVersion{}, false
	}
	if plus := strings.Index(value, "+"); plus >= 0 {
		value = value[:plus]
	}
	preRelease := ""
	if dash := strings.Index(value, "-"); dash >= 0 {
		preRelease = value[dash+1:]
		value = value[:dash]
	}
	parts := strings.Split(value, ".")
	numbers := make([]int, 0, len(parts))
	for _, part := range parts {
		if part == "" {
			return releaseVersion{}, false
		}
		n, err := strconv.Atoi(part)
		if err != nil {
			return releaseVersion{}, false
		}
		numbers = append(numbers, n)
	}
	return releaseVersion{numbers: numbers, preRelease: preRelease}, true
}

func upgradeAssetName(goos, goarch string) (string, error) {
	switch goarch {
	case "amd64", "arm64":
	default:
		return "", fmt.Errorf("unsupported architecture for upgrade: %s", goarch)
	}

	switch goos {
	case "darwin", "linux":
		return fmt.Sprintf("claude-switch-%s-%s.tar.gz", goos, goarch), nil
	case "windows":
		return fmt.Sprintf("claude-switch-%s-%s.zip", goos, goarch), nil
	default:
		return "", fmt.Errorf("unsupported OS for upgrade: %s", goos)
	}
}

func releaseDownloadURL(baseURL, repo, tag, asset string) string {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	repo = strings.Trim(strings.TrimSpace(repo), "/")
	asset = url.PathEscape(asset)
	if strings.TrimSpace(tag) == "" || strings.TrimSpace(tag) == "latest" {
		return fmt.Sprintf("%s/%s/releases/latest/download/%s", baseURL, repo, asset)
	}
	return fmt.Sprintf("%s/%s/releases/download/%s/%s", baseURL, repo, url.PathEscape(strings.TrimSpace(tag)), asset)
}

func downloadFile(ctx context.Context, client *http.Client, downloadURL, dest string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "claude-switch/"+version)

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("download failed: %s", resp.Status)
	}

	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	file, err := os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = io.Copy(file, resp.Body)
	return err
}

func extractTarGzBinary(archivePath, binaryName, dest string) error {
	file, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer file.Close()

	gz, err := gzip.NewReader(file)
	if err != nil {
		return err
	}
	defer gz.Close()

	reader := tar.NewReader(gz)
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		if header.Typeflag != tar.TypeReg || filepath.Base(header.Name) != binaryName {
			continue
		}
		return writeExtractedBinary(reader, dest)
	}
	return fmt.Errorf("archive does not contain %s", binaryName)
}

func extractZipBinary(archivePath, binaryName, dest string) error {
	reader, err := zip.OpenReader(archivePath)
	if err != nil {
		return err
	}
	defer reader.Close()

	for _, file := range reader.File {
		if file.FileInfo().IsDir() || filepath.Base(file.Name) != binaryName {
			continue
		}
		src, err := file.Open()
		if err != nil {
			return err
		}
		defer src.Close()
		return writeExtractedBinary(src, dest)
	}
	return fmt.Errorf("archive does not contain %s", binaryName)
}

func writeExtractedBinary(src io.Reader, dest string) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	file, err := os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o700)
	if err != nil {
		return err
	}
	if _, err := io.Copy(file, src); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}

func replaceExecutable(src, target string) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}

	bkF, err := os.CreateTemp(filepath.Dir(target), filepath.Base(target)+".old-*")
	if err != nil {
		return fmt.Errorf("create backup name: %w", err)
	}
	backup := bkF.Name()
	bkF.Close()
	os.Remove(backup)

	renamedExisting := false
	if _, err := os.Stat(target); err == nil {
		if err := os.Rename(target, backup); err != nil {
			return fmt.Errorf("prepare existing executable for replacement: %w", err)
		}
		renamedExisting = true
	} else if !os.IsNotExist(err) {
		return err
	}

	if err := moveFile(src, target); err != nil {
		if renamedExisting {
			if rbErr := os.Rename(backup, target); rbErr != nil {
				fmt.Fprintf(os.Stderr, "claude-switch: upgrade failed; rollback also failed (%v). Old binary saved at %s\n", rbErr, backup)
			}
		}
		return fmt.Errorf("install upgraded executable: %w", err)
	}
	if renamedExisting {
		if err := os.Remove(backup); err != nil {
			fmt.Fprintf(os.Stderr, "claude-switch: warning: could not remove backup %s: %v\n", backup, err)
		}
	}
	return nil
}

func moveFile(src, target string) error {
	if err := os.Rename(src, target); err == nil {
		return nil
	}
	if err := copyFile(src, target); err != nil {
		return err
	}
	_ = os.Remove(src)
	return nil
}

func copyFile(src, target string) error {
	source, err := os.Open(src)
	if err != nil {
		return err
	}
	defer source.Close()

	info, err := source.Stat()
	if err != nil {
		return err
	}
	dest, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, info.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(dest, source); err != nil {
		dest.Close()
		return err
	}
	return dest.Close()
}
