package launch

import (
	"os"
	"os/signal"
	"sync"
	"syscall"
)

var signalCleanupRegistry = struct {
	sync.Mutex
	started bool
	items   map[*LaunchedChrome]struct{}
}{
	items: make(map[*LaunchedChrome]struct{}),
}

func registerSignalCleanup(chrome *LaunchedChrome) {
	if chrome == nil {
		return
	}

	signalCleanupRegistry.Lock()
	defer signalCleanupRegistry.Unlock()

	signalCleanupRegistry.items[chrome] = struct{}{}
	if signalCleanupRegistry.started {
		return
	}

	signalCleanupRegistry.started = true
	go runSignalCleanupLoop()
}

func unregisterSignalCleanup(chrome *LaunchedChrome) {
	if chrome == nil {
		return
	}

	signalCleanupRegistry.Lock()
	delete(signalCleanupRegistry.items, chrome)
	signalCleanupRegistry.Unlock()
}

func runSignalCleanupLoop() {
	ch := make(chan os.Signal, 2)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)

	for sig := range ch {
		signalCleanupRegistry.Lock()
		items := make([]*LaunchedChrome, 0, len(signalCleanupRegistry.items))
		for chrome := range signalCleanupRegistry.items {
			items = append(items, chrome)
		}
		signalCleanupRegistry.Unlock()

		for _, chrome := range items {
			_ = chrome.Close()
		}

		signal.Reset(sig)
		if syscallSig, ok := sig.(syscall.Signal); ok {
			_ = syscall.Kill(os.Getpid(), syscallSig)
		}
		os.Exit(0)
	}
}
