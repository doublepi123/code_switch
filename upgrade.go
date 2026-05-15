package main

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
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

var errNoChecksumAvailable = errors.New("no checksum available (skipping verification)")

func cmdUpgrade(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("upgrade", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	repo := fs.String("repo", defaultUpgradeRepo, "GitHub repository in owner/repo form")
	tag := fs.String("tag", "", "release tag to install instead of latest")
	installPath := fs.String("install-path", "", "override target executable path")
	dryRun := fs.Bool("dry-run", false, "print the download URL and target path without installing")
	force := fs.Bool("force", false, "force upgrade even if already on the latest version")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("usage: code-switch upgrade [--tag vX.Y.Z] [--install-path PATH] [--force]")
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
		force:       *force,
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
	force       bool
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

	assets, err := upgradeAssetNames(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return err
	}
	asset := assets[0]
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
	if !opts.force && shouldSkipUpgrade(version, targetTag) {
		fmt.Fprintf(opts.out, "code-switch is already up to date (%s)\n", version)
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

	tmpDir, err := os.MkdirTemp("", "code-switch-upgrade-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpDir)

	canonicalAsset := assets[0]
	asset, archivePath, err := downloadUpgradeArchive(context.Background(), opts.client, opts.baseURL, opts.repo, targetTag, tmpDir, assets, opts.out)
	if err != nil {
		return err
	}

	if err := verifyAssetChecksum(context.Background(), opts.client, opts.baseURL, opts.repo, targetTag, asset, archivePath, canonicalAsset); err != nil {
		if errors.Is(err, errNoChecksumAvailable) {
			fmt.Fprintf(opts.out, "checksum: %v\n", err)
		} else {
			return fmt.Errorf("checksum verification failed: %w", err)
		}
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

	fmt.Fprintf(opts.out, "upgraded code-switch to latest release\n")
	return nil
}

func latestReleaseTag(ctx context.Context, client *http.Client, baseURL, repo string) (string, error) {
	latestURL := fmt.Sprintf("%s/%s/releases/latest", strings.TrimRight(strings.TrimSpace(baseURL), "/"), strings.Trim(strings.TrimSpace(repo), "/"))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, latestURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "code-switch/"+version)

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
		return fmt.Sprintf("code-switch-%s-%s.tar.gz", goos, goarch), nil
	case "windows":
		return fmt.Sprintf("code-switch-%s-%s.zip", goos, goarch), nil
	default:
		return "", fmt.Errorf("unsupported OS for upgrade: %s", goos)
	}
}

func upgradeAssetNames(goos, goarch string) ([]string, error) {
	asset, err := upgradeAssetName(goos, goarch)
	if err != nil {
		return nil, err
	}
	legacy := strings.Replace(asset, "code-switch-", "claude-switch-", 1)
	if legacy == asset {
		return []string{asset}, nil
	}
	return []string{asset, legacy}, nil
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

func downloadUpgradeArchive(ctx context.Context, client *http.Client, baseURL, repo, tag, tmpDir string, assets []string, out io.Writer) (string, string, error) {
	var lastErr error
	for i, asset := range assets {
		downloadURL := releaseDownloadURL(baseURL, repo, tag, asset)
		if i > 0 {
			fmt.Fprintf(out, "download fallback: %s\n", downloadURL)
		}
		archivePath := filepath.Join(tmpDir, asset)
		if err := downloadFile(ctx, client, downloadURL, archivePath); err != nil {
			lastErr = err
			if !isHTTPStatus(err, http.StatusNotFound) {
				return "", "", err
			}
			continue
		}
		return asset, archivePath, nil
	}
	return "", "", lastErr
}

type httpStatusError struct {
	operation  string
	status     string
	statusCode int
}

func (err *httpStatusError) Error() string {
	return fmt.Sprintf("%s failed: %s", err.operation, err.status)
}

func isHTTPStatus(err error, statusCode int) bool {
	var statusErr *httpStatusError
	return errors.As(err, &statusErr) && statusErr.statusCode == statusCode
}

func downloadFile(ctx context.Context, client *http.Client, downloadURL, dest string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "code-switch/"+version)

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &httpStatusError{operation: "download", status: resp.Status, statusCode: resp.StatusCode}
	}

	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	file, err := os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	const maxDownloadSize = 500 * 1024 * 1024
	written, err := io.Copy(file, io.LimitReader(resp.Body, maxDownloadSize))
	if err != nil {
		return err
	}
	if written == maxDownloadSize {
		return fmt.Errorf("download exceeded maximum size of %d bytes; file may be corrupted", maxDownloadSize)
	}
	return nil
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
		err = writeExtractedBinary(src, dest)
		src.Close()
		return err
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
				fmt.Fprintf(os.Stderr, "code-switch: upgrade failed; rollback also failed (%v). Old binary saved at %s\n", rbErr, backup)
			}
		}
		return fmt.Errorf("install upgraded executable: %w", err)
	}
	if renamedExisting {
		if err := os.Remove(backup); err != nil {
			fmt.Fprintf(os.Stderr, "code-switch: warning: could not remove backup %s: %v\n", backup, err)
		}
	}
	return nil
}

func moveFile(src, target string) error {
	if err := os.Rename(src, target); err == nil {
		return nil
	}
	// Cross-device moves require copy+delete; other rename errors are real failures.
	if err := copyFile(src, target); err != nil {
		return fmt.Errorf("move %s to %s: %w", src, target, err)
	}
	if err := os.Remove(src); err != nil {
		fmt.Fprintf(os.Stderr, "code-switch: warning: could not remove temp file %s: %v\n", src, err)
	}
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

func verifyAssetChecksum(ctx context.Context, client *http.Client, baseURL, repo, tag, asset, archivePath string, canonicalAsset ...string) error {
	type checksumEntry struct {
		hash string
		file string
	}

	parseChecksumFile := func(contents string) ([]checksumEntry, error) {
		lines := strings.Split(contents, "\n")
		var entries []checksumEntry
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			parts := strings.Fields(line)
			if len(parts) >= 2 {
				entries = append(entries, checksumEntry{hash: parts[0], file: parts[1]})
			}
		}
		if len(entries) == 0 {
			return nil, fmt.Errorf("no entries in checksum file")
		}
		return entries, nil
	}

	findEntry := func(entries []checksumEntry, asset string) *checksumEntry {
		for _, e := range entries {
			name := strings.TrimPrefix(e.file, "*")
			if name == asset || name == "./"+asset {
				return &e
			}
		}
		return nil
	}

	// Strategy 1: try <asset>.sha256 (just the hash)
	shaURL := releaseDownloadURL(baseURL, repo, tag, asset+".sha256")
	shaData, err := downloadChecksumContent(ctx, client, shaURL)
	if err == nil && strings.TrimSpace(shaData) != "" {
		expected := strings.TrimSpace(strings.Fields(shaData)[0])
		return validateSHA256(archivePath, expected)
	}

	// Strategy 2: try checksums.txt
	sumURL := releaseDownloadURL(baseURL, repo, tag, "checksums.txt")
	sumData, err := downloadChecksumContent(ctx, client, sumURL)
	if err == nil {
		entries, err := parseChecksumFile(sumData)
		if err != nil {
			return err
		}
		if entry := findEntry(entries, asset); entry != nil {
			return validateSHA256(archivePath, entry.hash)
		}
		// If the downloaded asset name differs from canonical (e.g. legacy fallback),
		// try the canonical name as well.
		if len(canonicalAsset) > 0 && canonicalAsset[0] != asset {
			if entry := findEntry(entries, canonicalAsset[0]); entry != nil {
				return validateSHA256(archivePath, entry.hash)
			}
		}
	}

	return errNoChecksumAvailable
}

func downloadChecksumContent(ctx context.Context, client *http.Client, urlStr string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, urlStr, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "code-switch/"+version)

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 65536))
	if err != nil {
		return "", err
	}
	return string(body), nil
}

func validateSHA256(filePath, expected string) error {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return fmt.Errorf("read file for checksum: %w", err)
	}
	actual := sha256.Sum256(data)
	actualHex := hex.EncodeToString(actual[:])
	if actualHex != expected {
		return fmt.Errorf("checksum mismatch: expected %s, got %s", expected, actualHex)
	}
	return nil
}
