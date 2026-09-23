package voice

import (
	"archive/tar"
	"compress/bzip2"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	ParakeetModelName   = "sherpa-onnx-nemo-parakeet-tdt-0.6b-v3-int8"
	parakeetModelURL    = "https://github.com/k2-fsa/sherpa-onnx/releases/download/asr-models/sherpa-onnx-nemo-parakeet-tdt-0.6b-v3-int8.tar.bz2"
	parakeetModelSHA256 = "5793d0fd397c5778d2cf2126994d58e9d56b1be7c04d13c7a15bb1b4eafb16bf"
)

// ModelFiles is the local Parakeet v3 model layout expected by sherpa-onnx.
type ModelFiles struct {
	Directory string
	Encoder   string
	Decoder   string
	Joiner    string
	Tokens    string
}

func modelFiles(directory string) ModelFiles {
	return ModelFiles{
		Directory: directory,
		Encoder:   filepath.Join(directory, "encoder.int8.onnx"),
		Decoder:   filepath.Join(directory, "decoder.int8.onnx"),
		Joiner:    filepath.Join(directory, "joiner.int8.onnx"),
		Tokens:    filepath.Join(directory, "tokens.txt"),
	}
}

func (m ModelFiles) valid() bool {
	for _, path := range []string{m.Encoder, m.Decoder, m.Joiner, m.Tokens} {
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
			return false
		}
	}
	return true
}

// EnsureParakeetModel returns the managed Parakeet v3 model, downloading and
// atomically installing the verified int8 package on first use.
func EnsureParakeetModel(ctx context.Context, configDir string) (ModelFiles, error) {
	// The model is large, so the overall timeout is generous, but a stalled
	// connection must not hang dictation startup forever, which is why this client
	// has timeouts where http.DefaultClient has none.
	client := &http.Client{
		Timeout: 10 * time.Minute,
		Transport: &http.Transport{
			DialContext:           (&net.Dialer{Timeout: 15 * time.Second}).DialContext,
			TLSHandshakeTimeout:   15 * time.Second,
			ResponseHeaderTimeout: 30 * time.Second,
			IdleConnTimeout:       60 * time.Second,
		},
	}
	return ensureParakeetModel(ctx, configDir, client, parakeetModelURL, parakeetModelSHA256)
}

func ensureParakeetModel(ctx context.Context, configDir string, client *http.Client, sourceURL, expectedSHA string) (ModelFiles, error) {
	destination := filepath.Join(configDir, "models", ParakeetModelName)
	files := modelFiles(destination)
	if files.valid() {
		return files, nil
	}
	parent := filepath.Dir(destination)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return ModelFiles{}, fmt.Errorf("create model directory: %w", err)
	}
	release, err := acquireModelLock(ctx, filepath.Join(parent, ".parakeet-v3.lock"), files)
	if err != nil {
		return ModelFiles{}, err
	}
	defer release()
	if files.valid() {
		return files, nil
	}
	staging, err := os.MkdirTemp(parent, ".parakeet-v3-")
	if err != nil {
		return ModelFiles{}, fmt.Errorf("create model staging directory: %w", err)
	}
	defer os.RemoveAll(staging)

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, sourceURL, nil)
	if err != nil {
		return ModelFiles{}, err
	}
	response, err := client.Do(request)
	if err != nil {
		return ModelFiles{}, fmt.Errorf("download parakeet v3: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return ModelFiles{}, fmt.Errorf("download parakeet v3: %s", response.Status)
	}

	archivePath := filepath.Join(staging, "model.tar.bz2")
	archive, err := os.OpenFile(archivePath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return ModelFiles{}, err
	}
	hash := sha256.New()
	_, copyErr := io.Copy(io.MultiWriter(archive, hash), response.Body)
	closeErr := archive.Close()
	if copyErr != nil {
		return ModelFiles{}, fmt.Errorf("download parakeet v3: %w", copyErr)
	}
	if closeErr != nil {
		return ModelFiles{}, closeErr
	}
	if actual := hex.EncodeToString(hash.Sum(nil)); !strings.EqualFold(actual, expectedSHA) {
		return ModelFiles{}, fmt.Errorf("parakeet v3 checksum mismatch: got %s", actual)
	}

	archive, err = os.Open(archivePath)
	if err != nil {
		return ModelFiles{}, err
	}
	extractRoot := filepath.Join(staging, "extract")
	if err := os.MkdirAll(extractRoot, 0o755); err != nil {
		_ = archive.Close()
		return ModelFiles{}, err
	}
	err = extractTar(tar.NewReader(bzip2.NewReader(archive)), extractRoot)
	_ = archive.Close()
	if err != nil {
		return ModelFiles{}, fmt.Errorf("extract Parakeet v3: %w", err)
	}
	stagedModel := filepath.Join(extractRoot, ParakeetModelName)
	if !modelFiles(stagedModel).valid() {
		return ModelFiles{}, errors.New("downloaded Parakeet v3 package is incomplete")
	}
	if err := removeIncompleteModel(destination, parent); err != nil {
		return ModelFiles{}, err
	}
	if err := os.Rename(stagedModel, destination); err != nil {
		return ModelFiles{}, fmt.Errorf("install Parakeet v3: %w", err)
	}
	return modelFiles(destination), nil
}

func acquireModelLock(ctx context.Context, path string, files ModelFiles) (func(), error) {
	for {
		lock, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			_, _ = fmt.Fprintf(lock, "%d\n", os.Getpid())
			return func() {
				_ = lock.Close()
				_ = os.Remove(path)
			}, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf("lock Parakeet v3 model: %w", err)
		}
		if files.valid() {
			return func() {}, nil
		}
		if info, statErr := os.Stat(path); statErr == nil && time.Since(info.ModTime()) > 2*time.Hour {
			_ = os.Remove(path)
			continue
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func removeIncompleteModel(destination, parent string) error {
	relative, err := filepath.Rel(parent, destination)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return errors.New("refusing to replace model outside the managed model directory")
	}
	if _, err := os.Stat(destination); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err := os.RemoveAll(destination); err != nil {
		return fmt.Errorf("replace incomplete Parakeet v3 model: %w", err)
	}
	return nil
}

func extractTar(reader *tar.Reader, destination string) error {
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		cleanName := filepath.Clean(filepath.FromSlash(header.Name))
		if cleanName == "." || filepath.IsAbs(cleanName) || cleanName == ".." || strings.HasPrefix(cleanName, ".."+string(filepath.Separator)) {
			return fmt.Errorf("unsafe archive path %q", header.Name)
		}
		target := filepath.Join(destination, cleanName)
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			file, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(file, reader)
			closeErr := file.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
		default:
			return fmt.Errorf("unsupported archive entry %q", header.Name)
		}
	}
}
