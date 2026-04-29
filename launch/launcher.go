package launch

import (
	"context"
	"errors"
)

func Launch(ctx context.Context, cfg BrowserConfig) (*LaunchResult, error) {
	switch cfg.Kind {
	case "", BrowserKindChromium:
		if cfg.Chromium == nil {
			cfg.Chromium = &LaunchLocalOptions{}
		}
		return LaunchLocalChrome(ctx, *cfg.Chromium)
	case BrowserKindKernel:
		if cfg.Kernel == nil {
			return nil, errors.New("kernel browser config is required")
		}
		return LaunchKernel(ctx, *cfg.Kernel)
	default:
		return nil, errors.New("unsupported browser kind: " + string(cfg.Kind))
	}
}
