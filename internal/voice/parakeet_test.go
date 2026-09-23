package voice

import (
	"archive/tar"
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

type fakeTranscriber struct {
	mu      sync.Mutex
	lengths []int
	closed  bool
}

func (f *fakeTranscriber) Transcribe(samples []float32) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lengths = append(f.lengths, len(samples))
	return "hello from parakeet", nil
}
func (f *fakeTranscriber) Close() { f.mu.Lock(); f.closed = true; f.mu.Unlock() }

type fakeMicrophone struct {
	mu      sync.Mutex
	stopped bool
}

func (f *fakeMicrophone) Start(callback func([]float32)) error {
	callback(make([]float32, parakeetSampleRate))
	return nil
}
func (f *fakeMicrophone) Stop() { f.mu.Lock(); f.stopped = true; f.mu.Unlock() }

func TestParakeetProcessWarmListenPauseStop(t *testing.T) {
	recognizer := &fakeTranscriber{}
	mic := &fakeMicrophone{}
	process := newParakeetProcess(parakeetDependencies{
		ensureModel:   func(context.Context) (ModelFiles, error) { return ModelFiles{}, nil },
		newRecognizer: func(ModelFiles) (transcriber, error) { return recognizer, nil },
		newCapture:    func() (microphone, error) { return mic, nil },
		interval:      5 * time.Millisecond,
	})
	events := make(chan Event, 8)
	go func() {
		scanner := bufio.NewScanner(process.Stdout())
		for scanner.Scan() {
			var event Event
			if json.Unmarshal(scanner.Bytes(), &event) == nil {
				events <- event
			}
		}
		close(events)
	}()

	wantEvent(t, events, "ready")
	writeCommand(t, process.Stdin(), "listen")
	wantEvent(t, events, "listening")
	partial := wantEvent(t, events, "partial")
	if partial.Text != "hello from parakeet" {
		t.Fatalf("partial text = %q", partial.Text)
	}
	writeCommand(t, process.Stdin(), "pause")
	wantEvent(t, events, "final")
	wantEvent(t, events, "paused")
	writeCommand(t, process.Stdin(), "stop")
	if err := process.Wait(); err != nil {
		t.Fatal(err)
	}
	mic.mu.Lock()
	stopped := mic.stopped
	mic.mu.Unlock()
	recognizer.mu.Lock()
	closed, calls := recognizer.closed, len(recognizer.lengths)
	recognizer.mu.Unlock()
	if !stopped || !closed || calls < 2 {
		t.Fatalf("stopped=%v closed=%v transcriptions=%d", stopped, closed, calls)
	}
}

func wantEvent(t *testing.T, events <-chan Event, kind string) Event {
	t.Helper()
	select {
	case event, ok := <-events:
		if !ok {
			t.Fatalf("event stream closed before %q", kind)
		}
		if event.Type != kind {
			t.Fatalf("event = %#v, want %q", event, kind)
		}
		return event
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for %q", kind)
		return Event{}
	}
}

func writeCommand(t *testing.T, writer io.Writer, kind string) {
	t.Helper()
	data, _ := json.Marshal(Event{Type: kind})
	if _, err := writer.Write(append(data, '\n')); err != nil {
		t.Fatal(err)
	}
}

func TestExistingParakeetModelNeedsNoDownload(t *testing.T) {
	configDir := t.TempDir()
	directory := filepath.Join(configDir, "models", ParakeetModelName)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"encoder.int8.onnx", "decoder.int8.onnx", "joiner.int8.onnx", "tokens.txt"} {
		if err := os.WriteFile(filepath.Join(directory, name), []byte("model"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	files, err := ensureParakeetModel(context.Background(), configDir, nil, "not-used", "not-used")
	if err != nil || files.Directory != directory {
		t.Fatalf("files=%#v err=%v", files, err)
	}
}

func TestExtractTarRejectsTraversal(t *testing.T) {
	var archive bytes.Buffer
	writer := tar.NewWriter(&archive)
	if err := writer.WriteHeader(&tar.Header{Name: "../escape", Mode: 0o644, Size: 1, Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := extractTar(tar.NewReader(bytes.NewReader(archive.Bytes())), t.TempDir()); err == nil {
		t.Fatal("unsafe archive path was accepted")
	}
}

func TestParakeetModelAndRecognizerIntegration(t *testing.T) {
	if os.Getenv("MIDAS_TEST_PARAKEET") != "1" {
		t.Skip("set MIDAS_TEST_PARAKEET=1 to download and load the Parakeet v3 model")
	}
	configDir := os.Getenv("MIDAS_CONFIG_DIR")
	if configDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			t.Fatal(err)
		}
		configDir = filepath.Join(home, ".midas")
	}
	files, err := EnsureParakeetModel(context.Background(), configDir)
	if err != nil {
		t.Fatal(err)
	}
	recognizer, err := newSherpaRecognizer(files)
	if err != nil {
		t.Fatal(err)
	}
	defer recognizer.Close()
	if _, err := recognizer.Transcribe(make([]float32, parakeetSampleRate)); err != nil {
		t.Fatal(err)
	}
}
