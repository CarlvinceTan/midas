package sse

import (
	"fmt"
	"strings"
	"testing"
)

func TestScanJoinsDataAndIgnoresOtherFields(t *testing.T) {
	t.Parallel()

	input := "event: message\ndata: one\ndata: two\n\n: keepalive\n\ndata: three\n"
	var events []string
	err := Scan(strings.NewReader(input), func(data []byte) error {
		events = append(events, string(data))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprint(events); got != "[one\ntwo three]" {
		t.Fatalf("events = %q", got)
	}
}
