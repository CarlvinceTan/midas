package tui

import (
	"bytes"
	"encoding/json"
	"os"
	"sync"
	"testing"
	"time"
)

type stdinBufferReference struct {
	Cases []stdinBufferCase `json:"cases"`
}

type stdinBufferOptionsReference struct {
	Timeout       int `json:"timeout"`
	EscapeTimeout int `json:"escapeTimeout"`
}

type stdinBufferEventReference struct {
	Type string `json:"type"`
	Data string `json:"data"`
}

type stdinBufferCase struct {
	Name      string                      `json:"name"`
	Chunks    []string                    `json:"chunks"`
	Options   stdinBufferOptionsReference `json:"options"`
	WaitMS    int                         `json:"waitMs"`
	Events    []stdinBufferEventReference `json:"events"`
	Remainder string                      `json:"remainder"`
}

func TestStdinBufferMatchesReference(t *testing.T) {
	data, err := os.ReadFile("testdata/stdin-buffer-reference.json")
	if err != nil {
		t.Fatal(err)
	}
	var reference stdinBufferReference
	if err := json.Unmarshal(data, &reference); err != nil {
		t.Fatal(err)
	}
	for _, test := range reference.Cases {
		t.Run(test.Name, func(t *testing.T) {
			var mu sync.Mutex
			events := make([]stdinBufferEventReference, 0)
			appendEvent := func(kind, data string) {
				mu.Lock()
				events = append(events, stdinBufferEventReference{Type: kind, Data: data})
				mu.Unlock()
			}
			buffer := newStdinBuffer(
				time.Duration(test.Options.Timeout)*time.Millisecond,
				time.Duration(test.Options.EscapeTimeout)*time.Millisecond,
				func(data string) { appendEvent("data", data) },
				func(data string) { appendEvent("paste", data) },
			)
			for _, chunk := range test.Chunks {
				buffer.Process([]byte(chunk))
			}
			if test.WaitMS > 0 {
				time.Sleep(time.Duration(test.WaitMS) * time.Millisecond)
			}
			actual := test
			mu.Lock()
			actual.Events = append([]stdinBufferEventReference(nil), events...)
			mu.Unlock()
			if len(actual.Events) == 0 {
				actual.Events = []stdinBufferEventReference{}
			}
			actual.Remainder = buffer.Buffer()
			buffer.Destroy()
			got, _ := json.Marshal(actual)
			want, _ := json.Marshal(test)
			if !bytes.Equal(got, want) {
				t.Fatalf("stdin buffer mismatch\ngot:  %s\nwant: %s", got, want)
			}
		})
	}
}

func TestStdinBufferDiscardDropsMaterializedEvents(t *testing.T) {
	events := make([]string, 0)
	var buffer *stdinBuffer
	buffer = newStdinBuffer(0, 0, func(data string) {
		events = append(events, data)
		if data == "q" {
			buffer.discardPendingEvents()
		}
	}, nil)
	buffer.Process([]byte("qrelease"))
	if len(events) != 1 || events[0] != "q" {
		t.Fatalf("events after discard = %#v, want [q]", events)
	}
}

func TestStdinBufferClearKeepsMaterializedEvents(t *testing.T) {
	events := make([]string, 0)
	var buffer *stdinBuffer
	buffer = newStdinBuffer(0, 0, func(data string) {
		events = append(events, data)
		if data == "q" {
			buffer.Clear()
		}
	}, nil)
	buffer.Process([]byte("qr"))
	if len(events) != 2 || events[0] != "q" || events[1] != "r" {
		t.Fatalf("events after Clear = %#v, want [q r]", events)
	}
}
