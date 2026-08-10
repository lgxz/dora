package dora

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestUnixInstallerDownloadsVerifiesAndInstalls(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("Unix installer supports macOS and Linux")
	}
	if runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64" {
		t.Skip("Unix installer supports amd64 and arm64")
	}

	root := t.TempDir()
	releaseDir := filepath.Join(root, "release")
	if err := os.Mkdir(releaseDir, 0o700); err != nil {
		t.Fatal(err)
	}
	archiveName := fmt.Sprintf("dora-%s-%s.tar.gz", runtime.GOOS, runtime.GOARCH)
	archivePath := filepath.Join(releaseDir, archiveName)
	writeInstallerTestArchive(t, archivePath)
	payload, err := os.ReadFile(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	checksum := sha256.Sum256(payload)
	if err := os.WriteFile(
		filepath.Join(releaseDir, "checksums.txt"),
		[]byte(fmt.Sprintf("%x  %s\n", checksum, archiveName)),
		0o600,
	); err != nil {
		t.Fatal(err)
	}

	fakeBin := filepath.Join(root, "fake-bin")
	if err := os.Mkdir(fakeBin, 0o700); err != nil {
		t.Fatal(err)
	}
	fakeCurl := `#!/bin/sh
set -eu
output=
url=
while [ "$#" -gt 0 ]; do
    case "$1" in
        -o) shift; output=$1 ;;
        https://*) url=$1 ;;
    esac
    shift
done
[ -n "$output" ] && [ -n "$url" ]
cp "$DORA_TEST_RELEASE_DIR/${url##*/}" "$output"
`
	if err := os.WriteFile(filepath.Join(fakeBin, "curl"), []byte(fakeCurl), 0o700); err != nil {
		t.Fatal(err)
	}

	rendered := filepath.Join(root, "rendered")
	render := exec.Command("sh", "scripts/render-installers.sh", "v1.2.3", rendered)
	if output, err := render.CombinedOutput(); err != nil {
		t.Fatalf("render installers: %v\n%s", err, output)
	}
	powerShellInstaller, err := os.ReadFile(filepath.Join(rendered, "dora-installer.ps1"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(powerShellInstaller), "__DORA_VERSION__") ||
		!strings.Contains(string(powerShellInstaller), `"v1.2.3"`) {
		t.Fatalf("PowerShell installer was not pinned: %s", powerShellInstaller)
	}
	if !strings.Contains(string(powerShellInstaller), `.dora-install.json`) ||
		!strings.Contains(string(powerShellInstaller), `"repository":"lgxz/dora"`) {
		t.Fatalf("PowerShell installer does not write the standalone marker: %s", powerShellInstaller)
	}
	invalidRender := exec.Command("sh", "scripts/render-installers.sh", "v1", filepath.Join(root, "invalid"))
	if err := invalidRender.Run(); err == nil {
		t.Fatal("invalid release version was accepted")
	}
	home := filepath.Join(root, "home")
	installer := exec.Command("sh", filepath.Join(rendered, "dora-installer.sh"))
	installer.Env = installerTestEnvironment(map[string]string{
		"DORA_RELEASE_BASE_URL": "https://example.test/releases/download",
		"DORA_TEST_RELEASE_DIR": releaseDir,
		"HOME":                  home,
		"PATH":                  fakeBin + string(os.PathListSeparator) + os.Getenv("PATH"),
	})
	output, err := installer.CombinedOutput()
	if err != nil {
		t.Fatalf("install: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "dora 1.2.3") {
		t.Fatalf("output = %q", output)
	}

	installed := filepath.Join(home, ".local", "bin", "dora")
	info, err := os.Stat(installed)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&0o111 == 0 {
		t.Fatalf("installed mode = %v", info.Mode())
	}
	marker, err := os.ReadFile(filepath.Join(home, ".local", "bin", ".dora-install.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(marker) != "{\"schema\":1,\"repository\":\"lgxz/dora\"}\n" {
		t.Fatalf("install marker = %q", marker)
	}
}

func writeInstallerTestArchive(t *testing.T, path string) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	gzipWriter := gzip.NewWriter(file)
	tarWriter := tar.NewWriter(gzipWriter)
	binary := []byte("#!/bin/sh\necho 'dora 1.2.3 (commit test, built today)'\n")
	if err := tarWriter.WriteHeader(&tar.Header{Name: "dora", Mode: 0o755, Size: int64(len(binary))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tarWriter.Write(binary); err != nil {
		t.Fatal(err)
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func installerTestEnvironment(overrides map[string]string) []string {
	environment := make([]string, 0, len(os.Environ())+len(overrides))
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if _, overridden := overrides[name]; !overridden && !strings.HasPrefix(name, "DORA_") {
			environment = append(environment, entry)
		}
	}
	for name, value := range overrides {
		environment = append(environment, name+"="+value)
	}
	return environment
}
