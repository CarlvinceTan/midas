package voice

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/gen2brain/malgo"
	sherpa "github.com/k2-fsa/sherpa-onnx-go/sherpa_onnx"
)

const (
	parakeetSampleRate     = 16000
	parakeetDecodeInterval = 1200 * time.Millisecond
)

// ParakeetOptions configures Midas's built-in Parakeet v3 dictation backend.
// ConfigDir is normally ~/.midas; the model is managed below it automatically.
type ParakeetOptions struct {
	ConfigDir string
	OnText    func(committed, partial string)
	OnError   func(string)
	OnReady   func()
	OnStop    func()
}

// NewParakeet returns a warm controller that always uses Parakeet-TDT 0.6B v3.
func NewParakeet(options ParakeetOptions) *Controller {
	dependencies := parakeetDependencies{
		ensureModel: func(ctx context.Context) (ModelFiles, error) {
			return EnsureParakeetModel(ctx, options.ConfigDir)
		},
		newRecognizer: newSherpaRecognizer,
		newCapture:    newMicrophoneCapture,
		interval:      parakeetDecodeInterval,
	}
	return New(Options{
		Command: "parakeet-v3",
		Spawn: func(string, []string) (Process, error) {
			return newParakeetProcess(dependencies), nil
		},
		OnText: options.OnText, OnError: options.OnError, OnReady: options.OnReady, OnStop: options.OnStop,
	})
}

type transcriber interface {
	Transcribe([]float32) (string, error)
	Close()
}

type microphone interface {
	Start(func([]float32)) error
	Stop()
}

type parakeetDependencies struct {
	ensureModel   func(context.Context) (ModelFiles, error)
	newRecognizer func(ModelFiles) (transcriber, error)
	newCapture    func() (microphone, error)
	interval      time.Duration
}

type parakeetProcess struct {
	context context.Context
	cancel  context.CancelFunc
	stdinR  *io.PipeReader
	stdinW  *io.PipeWriter
	stdoutR *io.PipeReader
	stdoutW *io.PipeWriter
	done    chan struct{}
	once    sync.Once
	deps    parakeetDependencies
}

func newParakeetProcess(dependencies parakeetDependencies) *parakeetProcess {
	ctx, cancel := context.WithCancel(context.Background())
	stdinR, stdinW := io.Pipe()
	stdoutR, stdoutW := io.Pipe()
	process := &parakeetProcess{context: ctx, cancel: cancel, stdinR: stdinR, stdinW: stdinW, stdoutR: stdoutR, stdoutW: stdoutW, done: make(chan struct{}), deps: dependencies}
	go process.run()
	return process
}

func (p *parakeetProcess) Stdout() io.Reader { return p.stdoutR }
func (p *parakeetProcess) Stdin() io.Writer  { return p.stdinW }
func (p *parakeetProcess) PID() int          { return os.Getpid() }
func (p *parakeetProcess) Wait() error       { <-p.done; return nil }
func (p *parakeetProcess) KillTree() error {
	p.stop()
	return nil
}
func (p *parakeetProcess) stop() {
	p.once.Do(func() {
		p.cancel()
		_ = p.stdinR.Close()
		_ = p.stdinW.Close()
	})
}

func (p *parakeetProcess) run() {
	defer close(p.done)
	defer p.stdoutW.Close()
	// Closing the pipes on every exit path releases the command reader: it blocks
	// in Scan until stdin closes, and a model or recognizer failure would
	// otherwise leave it running for the life of the process.
	defer p.stop()
	commands := make(chan string, 4)
	go readParakeetCommands(p.context, p.stdinR, commands)

	files, err := p.deps.ensureModel(p.context)
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			p.emit(Event{Type: "error", Message: err.Error()})
		}
		return
	}
	recognizer, err := p.deps.newRecognizer(files)
	if err != nil {
		p.emit(Event{Type: "error", Message: err.Error()})
		return
	}
	defer recognizer.Close()
	p.emit(Event{Type: "ready"})

	var capture microphone
	var audioMu sync.Mutex
	var audio []float32
	decodedSamples := 0
	interval := p.deps.interval
	if interval <= 0 {
		interval = parakeetDecodeInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-p.context.Done():
			if capture != nil {
				capture.Stop()
			}
			return
		case command, ok := <-commands:
			if !ok || command == "stop" {
				if capture != nil {
					capture.Stop()
				}
				return
			}
			switch command {
			case "listen":
				if capture != nil {
					continue
				}
				audioMu.Lock()
				audio = audio[:0]
				decodedSamples = 0
				audioMu.Unlock()
				capture, err = p.deps.newCapture()
				if err == nil {
					err = capture.Start(func(samples []float32) {
						audioMu.Lock()
						audio = append(audio, samples...)
						audioMu.Unlock()
					})
				}
				if err != nil {
					if capture != nil {
						capture.Stop()
						capture = nil
					}
					p.emit(Event{Type: "error", Message: "Could not open the microphone: " + err.Error()})
					return
				}
				p.emit(Event{Type: "listening"})
			case "pause":
				if capture == nil {
					continue
				}
				capture.Stop()
				capture = nil
				samples := copySamples(&audioMu, audio)
				if len(samples) > 0 {
					if text, decodeErr := recognizer.Transcribe(samples); decodeErr == nil {
						p.emit(Event{Type: "final", Text: text})
					}
				}
				p.emit(Event{Type: "paused"})
			}
		case <-ticker.C:
			if capture == nil {
				continue
			}
			audioMu.Lock()
			if len(audio) == decodedSamples || len(audio) < parakeetSampleRate/2 {
				audioMu.Unlock()
				continue
			}
			samples := append([]float32(nil), audio...)
			decodedSamples = len(audio)
			audioMu.Unlock()
			text, decodeErr := recognizer.Transcribe(samples)
			if decodeErr != nil {
				p.emit(Event{Type: "error", Message: decodeErr.Error()})
				if capture != nil {
					capture.Stop()
				}
				return
			}
			p.emit(Event{Type: "partial", Text: text})
		}
	}
}

func copySamples(mu *sync.Mutex, samples []float32) []float32 {
	mu.Lock()
	defer mu.Unlock()
	return append([]float32(nil), samples...)
}

func readParakeetCommands(ctx context.Context, input io.Reader, commands chan<- string) {
	defer close(commands)
	scanner := bufio.NewScanner(input)
	// The command pipe carries small JSON objects; the buffer is raised so a single
	// long line cannot silently end the stream.
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for scanner.Scan() {
		var event Event
		if json.Unmarshal(scanner.Bytes(), &event) != nil {
			continue
		}
		switch event.Type {
		case "listen", "pause", "stop":
			select {
			case commands <- event.Type:
			case <-ctx.Done():
				return
			}
		}
	}
	// The stream ending is how the caller learns the process is gone, whether it
	// ended because the pipe closed or because it could not be read; scanner.Err
	// only adds the reason, which this goroutine has no channel to carry.
	_ = scanner.Err()
}

func (p *parakeetProcess) emit(event Event) {
	data, err := json.Marshal(event)
	if err != nil {
		return
	}
	_, _ = p.stdoutW.Write(append(data, '\n'))
}

type sherpaRecognizer struct{ recognizer *sherpa.OfflineRecognizer }

func newSherpaRecognizer(files ModelFiles) (transcriber, error) {
	threads := runtime.NumCPU() / 2
	if threads < 1 {
		threads = 1
	}
	if threads > 4 {
		threads = 4
	}
	config := sherpa.OfflineRecognizerConfig{
		FeatConfig: sherpa.FeatureConfig{SampleRate: parakeetSampleRate, FeatureDim: 80},
		ModelConfig: sherpa.OfflineModelConfig{
			Transducer: sherpa.OfflineTransducerModelConfig{Encoder: files.Encoder, Decoder: files.Decoder, Joiner: files.Joiner},
			Tokens:     files.Tokens, NumThreads: threads, Provider: "cpu", ModelType: "nemo_transducer",
		},
		DecodingMethod: "greedy_search",
	}
	recognizer := sherpa.NewOfflineRecognizer(&config)
	if recognizer == nil {
		return nil, errors.New("could not load the built-in Parakeet v3 model")
	}
	return &sherpaRecognizer{recognizer: recognizer}, nil
}

func (r *sherpaRecognizer) Transcribe(samples []float32) (string, error) {
	if len(samples) == 0 {
		return "", nil
	}
	stream := sherpa.NewOfflineStream(r.recognizer)
	if stream == nil {
		return "", errors.New("could not create a Parakeet v3 recognition stream")
	}
	defer sherpa.DeleteOfflineStream(stream)
	stream.AcceptWaveform(parakeetSampleRate, samples)
	r.recognizer.Decode(stream)
	result := stream.GetResult()
	if result == nil {
		return "", errors.New("parakeet v3 returned no recognition result")
	}
	return strings.TrimSpace(result.Text), nil
}

func (r *sherpaRecognizer) Close() {
	if r.recognizer != nil {
		sherpa.DeleteOfflineRecognizer(r.recognizer)
		r.recognizer = nil
	}
}

type microphoneCapture struct {
	context *malgo.AllocatedContext
	device  *malgo.Device
	once    sync.Once
}

func newMicrophoneCapture() (microphone, error) { return &microphoneCapture{}, nil }

func (m *microphoneCapture) Start(onSamples func([]float32)) error {
	allocated, err := malgo.InitContext(nil, malgo.ContextConfig{}, func(string) {})
	if err != nil {
		return err
	}
	m.context = allocated
	config := malgo.DefaultDeviceConfig(malgo.Capture)
	config.Capture.Format = malgo.FormatF32
	config.Capture.Channels = 1
	config.SampleRate = parakeetSampleRate
	config.Alsa.NoMMap = 1
	device, err := malgo.InitDevice(allocated.Context, config, malgo.DeviceCallbacks{Data: func(_, input []byte, _ uint32) {
		if len(input) < 4 {
			return
		}
		samples := make([]float32, len(input)/4)
		for index := range samples {
			samples[index] = math.Float32frombits(binary.LittleEndian.Uint32(input[index*4:]))
		}
		onSamples(samples)
	}})
	if err != nil {
		m.Stop()
		return err
	}
	m.device = device
	if err := device.Start(); err != nil {
		m.Stop()
		return err
	}
	return nil
}

func (m *microphoneCapture) Stop() {
	m.once.Do(func() {
		if m.device != nil {
			m.device.Uninit()
			m.device = nil
		}
		if m.context != nil {
			_ = m.context.Uninit()
			m.context.Free()
			m.context = nil
		}
	})
}
