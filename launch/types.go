package launch

type BrowserKind string

const (
	BrowserKindChromium BrowserKind = "chromium"
	BrowserKindKernel   BrowserKind = "kernel"
)

type BrowserConfig struct {
	Kind     BrowserKind
	Chromium *LaunchLocalOptions
	Kernel   *LaunchKernelOptions
}

type ManagedBrowser interface {
	Close() error
}

type LaunchResult struct {
	WS       string
	Resource ManagedBrowser
	Chrome   *LaunchedChrome
	Kernel   *LaunchedKernel
}

type ConnectionTimeoutError struct {
	Message string
}

func (e *ConnectionTimeoutError) Error() string {
	return e.Message
}
