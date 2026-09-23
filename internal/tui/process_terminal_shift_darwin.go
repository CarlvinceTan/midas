//go:build darwin

package tui

import (
	"sync"

	"github.com/ebitengine/purego"
)

const coreGraphicsShiftMask = uint64(1 << 17)

var (
	loadCoreGraphicsFlagsOnce sync.Once
	coreGraphicsFlagsState    func(int32) uint64
)

func nativeShiftPressed() bool {
	loadCoreGraphicsFlagsOnce.Do(func() {
		handle, err := purego.Dlopen("/System/Library/Frameworks/CoreGraphics.framework/CoreGraphics", purego.RTLD_LAZY|purego.RTLD_LOCAL)
		if err != nil {
			return
		}
		symbol, err := purego.Dlsym(handle, "CGEventSourceFlagsState")
		if err != nil {
			return
		}
		purego.RegisterFunc(&coreGraphicsFlagsState, symbol)
	})
	return coreGraphicsFlagsState != nil && coreGraphicsFlagsState(0)&coreGraphicsShiftMask != 0
}
