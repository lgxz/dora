package update

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
)

func TestUpdateDownloadsVerifiesAndReplacesExecutable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test fixture is a Unix shell executable")
	}
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("release artifacts do not support this operating system")
	}
	if runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64" {
		t.Skip("release artifacts do not support this architecture")
	}

	directory := t.TempDir()
	executable := filepath.Join(directory, "dora")
	writeTestExecutable(t, executable, "1.0.0")
	writeTestMarker(t, directory)
	archiveName := fmt.Sprintf("dora-%s-%s.tar.gz", runtime.GOOS, runtime.GOARCH)
	archive := testTarGzip(t, "dora", testExecutable("1.1.0"))
	digest := sha256.Sum256(archive)
	manifest := fmt.Sprintf("%x  %s\n", digest, archiveName)

	client := &http.Client{Transport: updateRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		switch request.URL.Path {
		case "/repos/lgxz/dora/releases/latest":
			var body bytes.Buffer
			_ = json.NewEncoder(&body).Encode(map[string]any{
				"tag_name": "v1.1.0",
				"assets": []map[string]string{
					{"name": archiveName, "browser_download_url": "https://api.test/" + archiveName},
					{"name": "checksums.txt", "browser_download_url": "https://api.test/checksums.txt"},
				},
			})
			return updateResponse(body.Bytes()), nil
		case "/" + archiveName:
			return updateResponse(archive), nil
		case "/checksums.txt":
			return updateResponse([]byte(manifest)), nil
		default:
			return &http.Response{StatusCode: http.StatusNotFound, Status: "404 Not Found", Body: io.NopCloser(strings.NewReader("not found"))}, nil
		}
	})}

	service := New(Config{
		CurrentVersion: "1.0.0",
		HTTPClient:     client,
		APIBaseURL:     "https://api.test",
		ExecutablePath: func() (string, error) { return executable, nil },
	})
	result, err := service.Update(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result != (Result{Current: "1.0.0", Latest: "1.1.0", Updated: true}) {
		t.Fatalf("result = %#v", result)
	}
	output, err := exec.Command(executable, "--version").CombinedOutput()
	if err != nil {
		t.Fatalf("run updated executable: %v\n%s", err, output)
	}
	if !strings.HasPrefix(string(output), "dora 1.1.0 ") {
		t.Fatalf("version output = %q", output)
	}
	if _, err := os.Stat(filepath.Join(directory, ".dora.old")); !os.IsNotExist(err) {
		t.Fatalf("backup remains after update: %v", err)
	}
}

func TestUpdateDoesNotDownloadAssetsWhenCurrent(t *testing.T) {
	directory := t.TempDir()
	executable := filepath.Join(directory, "dora")
	writeTestExecutable(t, executable, "1.1.0")
	writeTestMarker(t, directory)
	var requests atomic.Int32
	client := &http.Client{Transport: updateRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests.Add(1)
		return updateResponse([]byte(`{"tag_name":"v1.1.0"}`)), nil
	})}

	result, err := New(Config{
		CurrentVersion: "1.1.0",
		HTTPClient:     client,
		APIBaseURL:     "https://api.test",
		ExecutablePath: func() (string, error) { return executable, nil },
	}).Update(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result != (Result{Current: "1.1.0", Latest: "1.1.0"}) {
		t.Fatalf("result = %#v", result)
	}
	if requests.Load() != 1 {
		t.Fatalf("requests = %d", requests.Load())
	}
}

func TestUpdateRejectsUnmanagedAndDevelopmentBuildsBeforeNetwork(t *testing.T) {
	directory := t.TempDir()
	executable := filepath.Join(directory, "dora")
	writeTestExecutable(t, executable, "1.0.0")

	_, err := New(Config{
		CurrentVersion: "1.0.0",
		ExecutablePath: func() (string, error) { return executable, nil },
	}).Update(context.Background())
	if err == nil || !strings.Contains(err.Error(), "standalone installer") {
		t.Fatalf("unmanaged error = %v", err)
	}

	_, err = New(Config{
		CurrentVersion: "dev",
		ExecutablePath: func() (string, error) {
			t.Fatal("development build looked up executable")
			return "", nil
		},
	}).Update(context.Background())
	if err == nil || !strings.Contains(err.Error(), `build "dev"`) {
		t.Fatalf("development error = %v", err)
	}
}

func TestUpdateChecksumFailurePreservesExecutable(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("test fixture uses a Unix release artifact")
	}
	directory := t.TempDir()
	executable := filepath.Join(directory, "dora")
	writeTestExecutable(t, executable, "1.0.0")
	writeTestMarker(t, directory)
	before, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	archiveName := fmt.Sprintf("dora-%s-%s.tar.gz", runtime.GOOS, runtime.GOARCH)
	archive := testTarGzip(t, "dora", testExecutable("1.1.0"))

	client := &http.Client{Transport: updateRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		switch request.URL.Path {
		case "/repos/lgxz/dora/releases/latest":
			var body bytes.Buffer
			_ = json.NewEncoder(&body).Encode(map[string]any{
				"tag_name": "v1.1.0",
				"assets": []map[string]string{
					{"name": archiveName, "browser_download_url": "https://api.test/archive"},
					{"name": "checksums.txt", "browser_download_url": "https://api.test/checksums"},
				},
			})
			return updateResponse(body.Bytes()), nil
		case "/archive":
			return updateResponse(archive), nil
		case "/checksums":
			return updateResponse([]byte(fmt.Sprintf("%064d  %s\n", 0, archiveName))), nil
		default:
			return &http.Response{StatusCode: http.StatusNotFound, Status: "404 Not Found", Body: io.NopCloser(strings.NewReader("not found"))}, nil
		}
	})}

	_, err = New(Config{
		CurrentVersion: "1.0.0",
		HTTPClient:     client,
		APIBaseURL:     "https://api.test",
		ExecutablePath: func() (string, error) { return executable, nil },
	}).Update(context.Background())
	if err == nil || !strings.Contains(err.Error(), "checksum verification failed") {
		t.Fatalf("error = %v", err)
	}
	after, readErr := os.ReadFile(executable)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("executable changed after checksum failure")
	}
}

func TestExtractZip(t *testing.T) {
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	file, err := writer.Create("dora.exe")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte("windows binary")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	got, err := extractBinary(buffer.Bytes(), "dora-windows-amd64.zip", "dora.exe")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "windows binary" {
		t.Fatalf("binary = %q", got)
	}
}

func writeTestMarker(t *testing.T, directory string) {
	t.Helper()
	data, err := json.Marshal(installMarker{Schema: 1, Repository: defaultRepository})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, installMarkerName), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeTestExecutable(t *testing.T, path, version string) {
	t.Helper()
	if err := os.WriteFile(path, testExecutable(version), 0o755); err != nil {
		t.Fatal(err)
	}
}

func testExecutable(version string) []byte {
	return []byte(fmt.Sprintf("#!/bin/sh\nprintf 'dora %s (commit test, built today)\\n'\n", version))
}

func testTarGzip(t *testing.T, name string, data []byte) []byte {
	t.Helper()
	var buffer bytes.Buffer
	gzipWriter := gzip.NewWriter(&buffer)
	tarWriter := tar.NewWriter(gzipWriter)
	if err := tarWriter.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(data))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tarWriter.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

type updateRoundTripFunc func(*http.Request) (*http.Response, error)

func (function updateRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func updateResponse(body []byte) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Status:     "200 OK",
		Body:       io.NopCloser(bytes.NewReader(body)),
	}
}
