// Package sse parses data fields from Server-Sent Events streams.
package sse

import (
	"bufio"
	"fmt"
	"io"
	"strings"
)

// Scan joins multi-line data fields and calls consume once per SSE event.
// Non-data fields and comments are intentionally ignored.
func Scan(reader io.Reader, consume func([]byte) error) error {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64<<10), 16<<20)
	var data []string
	flush := func() error {
		if len(data) == 0 {
			return nil
		}
		joined := []byte(strings.Join(data, "\n"))
		data = data[:0]
		return consume(joined)
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if err := flush(); err != nil {
				return err
			}
			continue
		}
		if strings.HasPrefix(line, "data:") {
			data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		} else if line == "data" {
			// A field with no colon has an empty value, so this is an empty data
			// line rather than a line to ignore.
			data = append(data, "")
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read SSE stream: %w", err)
	}
	return flush()
}
