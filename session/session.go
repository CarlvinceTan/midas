package session

import (
	"context"
	"sync"
	"time"

	"github.com/PolymuxOrg/midas/browser"
	"github.com/PolymuxOrg/midas/internal/debug"
	"github.com/PolymuxOrg/midas/launch"
)

type Options struct {
	WSURL                    string
	Headers                  map[string]string
	UserAgent                string
	EnsureFirstTopLevelPage  bool
	FirstTopLevelPageTimeout time.Duration
	Browser                  launch.BrowserConfig
}

type Session struct {
	ctx      *browser.Context
	resource launch.ManagedBrowser

	closeOnce sync.Once
	closeErr  error
}

func New(ctx context.Context, opts Options) (*Session, error) {
	wsURL := opts.WSURL
	var resource launch.ManagedBrowser
	if wsURL == "" {
		debug.Printf("session: launching local browser")
		result, err := launch.Launch(ctx, opts.Browser)
		if err != nil {
			return nil, err
		}
		wsURL = result.WS
		resource = result.Resource
		debug.Printf("session: launch complete, connecting CDP to %s", debug.WSSummary(wsURL))
	} else {
		debug.Printf("session: using existing browser WebSocket %s", debug.WSSummary(wsURL))
	}

	bctx, err := browser.Connect(ctx, wsURL, browser.ConnectOptions{
		Headers:                    opts.Headers,
		UserAgent:                  opts.UserAgent,
		EnsureFirstTopLevelPage:    opts.EnsureFirstTopLevelPage,
		FirstTopLevelPageTimeoutMs: int(opts.FirstTopLevelPageTimeout / time.Millisecond),
	})
	if err != nil {
		if resource != nil {
			_ = resource.Close()
		}
		return nil, err
	}

	return &Session{
		ctx:      bctx,
		resource: resource,
	}, nil
}

func (s *Session) Context() *browser.Context {
	if s == nil {
		return nil
	}
	return s.ctx
}

func (s *Session) Close() error {
	if s == nil {
		return nil
	}
	s.closeOnce.Do(func() {
		if s.ctx != nil {
			if closeErr := s.ctx.Close(); s.closeErr == nil {
				s.closeErr = closeErr
			}
		}
		if s.resource != nil {
			if closeErr := s.resource.Close(); s.closeErr == nil {
				s.closeErr = closeErr
			}
		}
	})
	return s.closeErr
}
