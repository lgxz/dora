// Package update implements release-based updates for standalone Dora installs.
package update

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"golang.org/x/mod/semver"
)

const (
	defaultAPIBaseURL = "https://api.github.com"
	defaultRepository = "lgxz/dora"
	installMarkerName = ".dora-install.json"
	maxArchiveBytes   = 128 << 20
	maxChecksumBytes  = 1 << 20
	maxReleaseBytes   = 1 << 20
)

// Result describes the outcome of an update attempt.
type Result struct {
	Current string
	Latest  string
	Updated bool
}

// Config contains update dependencies and test overrides.
type Config struct {
	CurrentVersion string
	HTTPClient     *http.Client
	APIBaseURL     string
	Repository     string
	ExecutablePath func() (string, error)
	OS             string
	Arch           string
	AllowInsecure  bool
}

// Service updates a standalone Dora executable from GitHub Releases.
type Service struct {
	config Config
}

// New constructs an update service.
func New(config Config) *Service {
	if config.HTTPClient == nil {
		config.HTTPClient = &http.Client{Timeout: 2 * time.Minute}
	}
	if config.APIBaseURL == "" {
		config.APIBaseURL = defaultAPIBaseURL
	}
	if config.Repository == "" {
		config.Repository = defaultRepository
	}
	if config.ExecutablePath == nil {
		config.ExecutablePath = os.Executable
	}
	if config.OS == "" {
		config.OS = runtime.GOOS
	}
	if config.Arch == "" {
		config.Arch = runtime.GOARCH
	}
	return &Service{config: config}
}

type release struct {
	TagName string `json:"tag_name"`
	Assets  []struct {
		Name               string `json:"name"`
		BrowserDownloadURL string `json:"browser_download_url"`
	} `json:"assets"`
}

type installMarker struct {
	Schema     int    `json:"schema"`
	Repository string `json:"repository"`
}

// Update installs the latest stable release when it is newer than the running version.
func (s *Service) Update(ctx context.Context) (Result, error) {
	current, err := normalizeVersion(s.config.CurrentVersion)
	if err != nil {
		return Result{}, fmt.Errorf("self-update is unavailable for build %q", s.config.CurrentVersion)
	}

	executable, err := s.config.ExecutablePath()
	if err != nil {
		return Result{}, fmt.Errorf("locate executable: %w", err)
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		return Result{}, fmt.Errorf("resolve executable: %w", err)
	}
	if err := validateInstallMarker(filepath.Dir(executable), s.config.Repository); err != nil {
		return Result{}, err
	}

	rel, err := s.latestRelease(ctx)
	if err != nil {
		return Result{}, err
	}
	latest, err := normalizeVersion(rel.TagName)
	if err != nil {
		return Result{}, fmt.Errorf("latest release has invalid version %q", rel.TagName)
	}
	result := Result{Current: strings.TrimPrefix(current, "v"), Latest: strings.TrimPrefix(latest, "v")}
	if semver.Compare(current, latest) >= 0 {
		return result, nil
	}

	archiveName, binaryName, err := artifactNames(s.config.OS, s.config.Arch)
	if err != nil {
		return Result{}, err
	}
	archiveURL, ok := assetURL(rel, archiveName)
	if !ok {
		return Result{}, fmt.Errorf("release %s does not contain %s", rel.TagName, archiveName)
	}
	checksumsURL, ok := assetURL(rel, "checksums.txt")
	if !ok {
		return Result{}, fmt.Errorf("release %s does not contain checksums.txt", rel.TagName)
	}

	checksums, err := s.download(ctx, checksumsURL, maxChecksumBytes)
	if err != nil {
		return Result{}, fmt.Errorf("download checksums: %w", err)
	}
	expected, err := checksumFor(checksums, archiveName)
	if err != nil {
		return Result{}, err
	}
	archive, err := s.download(ctx, archiveURL, maxArchiveBytes)
	if err != nil {
		return Result{}, fmt.Errorf("download %s: %w", archiveName, err)
	}
	actual := sha256.Sum256(archive)
	if subtle.ConstantTimeCompare(actual[:], expected) != 1 {
		return Result{}, fmt.Errorf("checksum verification failed for %s", archiveName)
	}
	binary, err := extractBinary(archive, archiveName, binaryName)
	if err != nil {
		return Result{}, err
	}
	if err := replaceExecutable(ctx, executable, binary, result.Latest); err != nil {
		return Result{}, err
	}
	result.Updated = true
	return result, nil
}

func (s *Service) latestRelease(ctx context.Context) (release, error) {
	endpoint := strings.TrimRight(s.config.APIBaseURL, "/") + "/repos/" + s.config.Repository + "/releases/latest"
	data, err := s.download(ctx, endpoint, maxReleaseBytes)
	if err != nil {
		return release{}, fmt.Errorf("query latest release: %w", err)
	}
	var rel release
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&rel); err != nil {
		return release{}, fmt.Errorf("decode latest release: %w", err)
	}
	if rel.TagName == "" {
		return release{}, errors.New("latest release is missing a tag")
	}
	return rel, nil
}

func (s *Service) download(ctx context.Context, rawURL string, limit int64) ([]byte, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("invalid URL: %w", err)
	}
	if parsed.Scheme != "https" && !s.config.AllowInsecure {
		return nil, fmt.Errorf("refusing non-HTTPS URL %q", rawURL)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, http.NoBody)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "dora/"+s.config.CurrentVersion)
	response, err := s.config.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.Request != nil && response.Request.URL != nil &&
		response.Request.URL.Scheme != "https" && !s.config.AllowInsecure {
		return nil, fmt.Errorf("refusing redirect to non-HTTPS URL %q", response.Request.URL.String())
	}
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP status %s", response.Status)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("response exceeds %d bytes", limit)
	}
	return data, nil
}

func normalizeVersion(value string) (string, error) {
	value = strings.TrimSpace(value)
	if !strings.HasPrefix(value, "v") {
		value = "v" + value
	}
	if !semver.IsValid(value) {
		return "", errors.New("invalid semantic version")
	}
	return value, nil
}

func validateInstallMarker(directory, repository string) error {
	data, err := os.ReadFile(filepath.Join(directory, installMarkerName))
	if errors.Is(err, os.ErrNotExist) {
		return errors.New("self-update is unavailable for this installation; reinstall with the standalone installer or use its package manager")
	}
	if err != nil {
		return fmt.Errorf("read standalone install marker: %w", err)
	}
	var marker installMarker
	if err := json.Unmarshal(data, &marker); err != nil {
		return fmt.Errorf("read standalone install marker: %w", err)
	}
	if marker.Schema != 1 || marker.Repository != repository {
		return errors.New("standalone install marker is invalid")
	}
	return nil
}

func artifactNames(goos, goarch string) (archive, binary string, err error) {
	if goarch != "amd64" && goarch != "arm64" {
		return "", "", fmt.Errorf("self-update does not support architecture %s", goarch)
	}
	switch goos {
	case "darwin", "linux":
		return fmt.Sprintf("dora-%s-%s.tar.gz", goos, goarch), "dora", nil
	case "windows":
		return fmt.Sprintf("dora-windows-%s.zip", goarch), "dora.exe", nil
	default:
		return "", "", fmt.Errorf("self-update does not support operating system %s", goos)
	}
}

func assetURL(rel release, name string) (string, bool) {
	for _, asset := range rel.Assets {
		if asset.Name == name && asset.BrowserDownloadURL != "" {
			return asset.BrowserDownloadURL, true
		}
	}
	return "", false
}

func checksumFor(manifest []byte, filename string) ([]byte, error) {
	for _, line := range strings.Split(string(manifest), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || strings.TrimPrefix(fields[1], "*") != filename {
			continue
		}
		checksum, err := hex.DecodeString(fields[0])
		if err != nil || len(checksum) != sha256.Size {
			return nil, fmt.Errorf("invalid checksum for %s", filename)
		}
		return checksum, nil
	}
	return nil, fmt.Errorf("checksum for %s is missing", filename)
}

func extractBinary(archive []byte, archiveName, binaryName string) ([]byte, error) {
	if strings.HasSuffix(archiveName, ".tar.gz") {
		return extractTarGzip(archive, binaryName)
	}
	if strings.HasSuffix(archiveName, ".zip") {
		return extractZip(archive, binaryName)
	}
	return nil, fmt.Errorf("unsupported release archive %s", archiveName)
}

func extractTarGzip(archive []byte, binaryName string) ([]byte, error) {
	gzipReader, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, fmt.Errorf("open release archive: %w", err)
	}
	defer gzipReader.Close()
	tarReader := tar.NewReader(gzipReader)
	for {
		header, err := tarReader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read release archive: %w", err)
		}
		if header.Name != binaryName {
			continue
		}
		if !header.FileInfo().Mode().IsRegular() || header.Size < 0 || header.Size > maxArchiveBytes {
			return nil, fmt.Errorf("release archive contains invalid %s", binaryName)
		}
		return io.ReadAll(io.LimitReader(tarReader, header.Size))
	}
	return nil, fmt.Errorf("release archive does not contain %s", binaryName)
}

func extractZip(archive []byte, binaryName string) ([]byte, error) {
	reader, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		return nil, fmt.Errorf("open release archive: %w", err)
	}
	for _, file := range reader.File {
		if file.Name != binaryName {
			continue
		}
		if !file.FileInfo().Mode().IsRegular() || file.UncompressedSize64 > maxArchiveBytes {
			return nil, fmt.Errorf("release archive contains invalid %s", binaryName)
		}
		stream, err := file.Open()
		if err != nil {
			return nil, fmt.Errorf("open %s in release archive: %w", binaryName, err)
		}
		data, readErr := io.ReadAll(io.LimitReader(stream, maxArchiveBytes+1))
		closeErr := stream.Close()
		if readErr != nil {
			return nil, fmt.Errorf("read %s in release archive: %w", binaryName, readErr)
		}
		if closeErr != nil {
			return nil, fmt.Errorf("close %s in release archive: %w", binaryName, closeErr)
		}
		if len(data) > maxArchiveBytes {
			return nil, fmt.Errorf("release archive contains oversized %s", binaryName)
		}
		return data, nil
	}
	return nil, fmt.Errorf("release archive does not contain %s", binaryName)
}

func replaceExecutable(ctx context.Context, target string, binary []byte, expectedVersion string) error {
	lockPath := filepath.Join(filepath.Dir(target), ".dora-update.lock")
	lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if errors.Is(err, os.ErrExist) {
		return errors.New("another update is in progress; remove .dora-update.lock if no updater is running")
	}
	if err != nil {
		return fmt.Errorf("create update lock: %w", err)
	}
	if _, err := fmt.Fprintf(lock, "%d\n", os.Getpid()); err != nil {
		_ = lock.Close()
		_ = os.Remove(lockPath)
		return fmt.Errorf("write update lock: %w", err)
	}
	if err := lock.Close(); err != nil {
		_ = os.Remove(lockPath)
		return fmt.Errorf("close update lock: %w", err)
	}
	defer os.Remove(lockPath)

	info, err := os.Stat(target)
	if err != nil {
		return fmt.Errorf("inspect current executable: %w", err)
	}
	pattern := ".dora-update-*"
	if runtime.GOOS == "windows" {
		pattern += ".exe"
	}
	staged, err := os.CreateTemp(filepath.Dir(target), pattern)
	if err != nil {
		return fmt.Errorf("stage updated executable: %w", err)
	}
	stagedPath := staged.Name()
	cleanupStaged := true
	defer func() {
		if cleanupStaged {
			_ = os.Remove(stagedPath)
		}
	}()
	if err := staged.Chmod(info.Mode().Perm()); err != nil {
		_ = staged.Close()
		return fmt.Errorf("set updated executable permissions: %w", err)
	}
	if _, err := staged.Write(binary); err != nil {
		_ = staged.Close()
		return fmt.Errorf("write updated executable: %w", err)
	}
	if err := staged.Sync(); err != nil {
		_ = staged.Close()
		return fmt.Errorf("sync updated executable: %w", err)
	}
	if err := staged.Close(); err != nil {
		return fmt.Errorf("close updated executable: %w", err)
	}
	output, err := exec.CommandContext(ctx, stagedPath, "--version").CombinedOutput()
	if err != nil {
		return fmt.Errorf("validate updated executable: %w", err)
	}
	fields := strings.Fields(string(output))
	if len(fields) < 2 || fields[0] != "dora" || fields[1] != expectedVersion {
		return fmt.Errorf("updated executable reports unexpected version %q", strings.TrimSpace(string(output)))
	}

	backup := filepath.Join(filepath.Dir(target), "."+filepath.Base(target)+".old")
	if err := os.Remove(backup); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove previous update backup: %w", err)
	}
	if err := os.Rename(target, backup); err != nil {
		return fmt.Errorf("back up current executable: %w", err)
	}
	if err := os.Rename(stagedPath, target); err != nil {
		rollbackErr := os.Rename(backup, target)
		if rollbackErr != nil {
			return fmt.Errorf("install updated executable: %v; rollback failed: %w", err, rollbackErr)
		}
		return fmt.Errorf("install updated executable: %w", err)
	}
	cleanupStaged = false
	_ = os.Remove(backup)
	return nil
}
