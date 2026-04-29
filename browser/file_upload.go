package browser

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

func normalizeFilePayloads(paths []string) ([]FilePayload, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	out := make([]FilePayload, 0, len(paths))
	for _, path := range paths {
		if path == "" {
			continue
		}
		abs, err := filepath.Abs(path)
		if err != nil {
			return nil, err
		}
		info, err := os.Stat(abs)
		if err != nil {
			if os.IsNotExist(err) {
				return nil, fmt.Errorf("setInputFiles(): file not found at %s", abs)
			}
			return nil, err
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("setInputFiles(): expected a regular file at %s", abs)
		}
		out = append(out, FilePayload{
			Name:         filepath.Base(abs),
			MIMEType:     "application/octet-stream",
			LastModified: info.ModTime().UnixMilli(),
			Buffer:       nil,
		})
	}
	return out, nil
}

func coerceFileTimestamp(ms int64) int64 {
	if ms > 0 {
		return ms
	}
	return time.Now().UnixMilli()
}
