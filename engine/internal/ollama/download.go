package ollama

import (
	"archive/tar"
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"
)

// Official release assets. The tarball is the manual-install artifact
// (bin/ollama + lib/ollama), which is exactly what a rootless install
// needs — no installer script involved. Since ~v0.13 the Linux archives are
// zstd-compressed.
const releaseBase = "https://github.com/ollama/ollama/releases/latest/download/"

func assetName() (string, error) {
	if runtime.GOOS != "linux" {
		return "", fmt.Errorf("managed ollama install only supported on linux (got %s)", runtime.GOOS)
	}
	switch runtime.GOARCH {
	case "amd64", "arm64":
		return "ollama-linux-" + runtime.GOARCH + ".tar.zst", nil
	}
	return "", fmt.Errorf("unsupported architecture %s", runtime.GOARCH)
}

// DownloadProgress reports rootless-install progress to the wizard.
type DownloadProgress struct {
	Phase     string `json:"phase"` // "download" | "verify" | "extract"
	Completed int64  `json:"completed"`
	Total     int64  `json:"total"`
}

// Download fetches the official Ollama tarball into Dir and extracts it,
// verifying the archive against the release's published sha256sum.txt.
func (m *Manager) Download(ctx context.Context, progress func(DownloadProgress)) error {
	asset, err := assetName()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(m.Dir, 0o755); err != nil {
		return err
	}

	wantSum, err := fetchChecksum(ctx, asset)
	if err != nil {
		return fmt.Errorf("fetch checksum: %w", err)
	}

	tarPath := filepath.Join(m.Dir, asset)
	if err := downloadFile(ctx, releaseBase+asset, tarPath, wantSum, progress); err != nil {
		return err
	}
	defer os.Remove(tarPath)

	if progress != nil {
		progress(DownloadProgress{Phase: "extract"})
	}
	if err := extractTarZst(tarPath, m.Dir); err != nil {
		return fmt.Errorf("extract: %w", err)
	}
	if !fileExists(m.managedBinary()) {
		return fmt.Errorf("archive extracted but %s is missing", m.managedBinary())
	}
	return nil
}

func fetchChecksum(ctx context.Context, asset string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, releaseBase+"sha256sum.txt", nil)
	if err != nil {
		return "", err
	}
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	sc := bufio.NewScanner(io.LimitReader(resp.Body, 1<<20))
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) == 2 && strings.TrimPrefix(fields[1], "./") == asset {
			return fields[0], nil
		}
	}
	return "", fmt.Errorf("no checksum entry for %s", asset)
}

func downloadFile(ctx context.Context, url, dest, wantSum string, progress func(DownloadProgress)) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := (&http.Client{}).Do(req) // no timeout: multi-GB download
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: HTTP %d", url, resp.StatusCode)
	}

	f, err := os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()

	hash := sha256.New()
	var done int64
	total := resp.ContentLength
	buf := make([]byte, 1<<20)
	lastReport := time.Now()
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := f.Write(buf[:n]); werr != nil {
				return werr
			}
			hash.Write(buf[:n])
			done += int64(n)
			if progress != nil && (time.Since(lastReport) > 200*time.Millisecond || rerr == io.EOF) {
				progress(DownloadProgress{Phase: "download", Completed: done, Total: total})
				lastReport = time.Now()
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return rerr
		}
	}

	if progress != nil {
		progress(DownloadProgress{Phase: "verify", Completed: done, Total: total})
	}
	if got := hex.EncodeToString(hash.Sum(nil)); got != wantSum {
		os.Remove(dest)
		return fmt.Errorf("checksum mismatch for %s: got %s want %s", filepath.Base(dest), got, wantSum)
	}
	return nil
}

// extractTarZst unpacks archive into destDir, refusing path traversal and
// absolute paths. Symlinks are restricted to targets inside destDir.
func extractTarZst(archive, destDir string) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	zr, err := zstd.NewReader(f)
	if err != nil {
		return err
	}
	defer zr.Close()

	tr := tar.NewReader(zr)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		name := filepath.Clean(hdr.Name)
		if filepath.IsAbs(name) || strings.HasPrefix(name, "..") {
			return fmt.Errorf("archive entry escapes destination: %q", hdr.Name)
		}
		target := filepath.Join(destDir, name)

		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, os.FileMode(hdr.Mode)&0o777)
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, tr); err != nil {
				out.Close()
				return err
			}
			out.Close()
		case tar.TypeSymlink:
			linkDest := filepath.Join(filepath.Dir(target), hdr.Linkname)
			if rel, err := filepath.Rel(destDir, linkDest); err != nil || strings.HasPrefix(rel, "..") {
				return fmt.Errorf("symlink escapes destination: %q -> %q", hdr.Name, hdr.Linkname)
			}
			os.Remove(target)
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			if err := os.Symlink(hdr.Linkname, target); err != nil {
				return err
			}
		case tar.TypeLink:
			src := filepath.Join(destDir, filepath.Clean(hdr.Linkname))
			if rel, err := filepath.Rel(destDir, src); err != nil || strings.HasPrefix(rel, "..") {
				return fmt.Errorf("hardlink escapes destination: %q", hdr.Linkname)
			}
			os.Remove(target)
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			if err := os.Link(src, target); err != nil {
				return err
			}
		}
	}
}
