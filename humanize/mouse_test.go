package humanize

import (
	"context"
	"math"
	"testing"
)

type recordedMove struct {
	X, Y float64
}

type fakeMouse struct {
	moves   []recordedMove
	downs   int
	ups     int
	wheels  []recordedMove
	failNth int // 1-based; 0 = never fail
	calls   int
}

func (f *fakeMouse) bump() error {
	f.calls++
	if f.failNth > 0 && f.calls == f.failNth {
		return context.Canceled
	}
	return nil
}

func (f *fakeMouse) Move(_ context.Context, x, y float64) error {
	if err := f.bump(); err != nil {
		return err
	}
	f.moves = append(f.moves, recordedMove{x, y})
	return nil
}

func (f *fakeMouse) Down(_ context.Context) error {
	if err := f.bump(); err != nil {
		return err
	}
	f.downs++
	return nil
}

func (f *fakeMouse) Up(_ context.Context) error {
	if err := f.bump(); err != nil {
		return err
	}
	f.ups++
	return nil
}

func (f *fakeMouse) Wheel(_ context.Context, dx, dy float64) error {
	if err := f.bump(); err != nil {
		return err
	}
	f.wheels = append(f.wheels, recordedMove{dx, dy})
	return nil
}

func TestEaseInOutEndpoints(t *testing.T) {
	if got := easeInOut(0); got != 0 {
		t.Errorf("easeInOut(0) = %v, want 0", got)
	}
	if got := easeInOut(1); got != 1 {
		t.Errorf("easeInOut(1) = %v, want 1", got)
	}
	if got := easeInOut(0.5); math.Abs(got-0.5) > 1e-9 {
		t.Errorf("easeInOut(0.5) = %v, want 0.5", got)
	}
}

func TestEaseInOutMonotonic(t *testing.T) {
	prev := easeInOut(0)
	for i := 1; i <= 50; i++ {
		t_ := float64(i) / 50
		cur := easeInOut(t_)
		if cur < prev {
			t.Fatalf("easeInOut not monotonic at t=%v: %v < %v", t_, cur, prev)
		}
		prev = cur
	}
}

func TestBezierEndpoints(t *testing.T) {
	p0 := Point{0, 0}
	p3 := Point{100, 50}
	cp1 := Point{30, 80}
	cp2 := Point{70, -20}
	if got := bezier(p0, cp1, cp2, p3, 0); got != p0 {
		t.Errorf("bezier(t=0) = %+v, want %+v", got, p0)
	}
	if got := bezier(p0, cp1, cp2, p3, 1); got != p3 {
		t.Errorf("bezier(t=1) = %+v, want %+v", got, p3)
	}
}

func TestMoveEmitsBezierTrail(t *testing.T) {
	cfg := DefaultConfig()
	fake := &fakeMouse{}
	if err := Move(context.Background(), fake, 0, 0, 800, 400, cfg); err != nil {
		t.Fatalf("Move returned error: %v", err)
	}
	if len(fake.moves) < cfg.MouseMinSteps {
		t.Errorf("Move emitted %d events; expected >= MouseMinSteps (%d)", len(fake.moves), cfg.MouseMinSteps)
	}
	// Last move should be near the target (within wobble + overshoot tolerance).
	last := fake.moves[len(fake.moves)-1]
	if math.Hypot(last.X-800, last.Y-400) > 20 {
		t.Errorf("final move %+v not near target (800,400)", last)
	}
}

func TestMoveSkipsZeroDistance(t *testing.T) {
	fake := &fakeMouse{}
	if err := Move(context.Background(), fake, 100, 100, 100, 100, DefaultConfig()); err != nil {
		t.Fatalf("Move returned error: %v", err)
	}
	if len(fake.moves) != 0 {
		t.Errorf("Move with zero distance emitted %d events; want 0", len(fake.moves))
	}
}

func TestMovePropagatesError(t *testing.T) {
	fake := &fakeMouse{failNth: 1}
	if err := Move(context.Background(), fake, 0, 0, 200, 200, DefaultConfig()); err == nil {
		t.Errorf("Move should propagate raw.Move error")
	}
}

func TestClickSequence(t *testing.T) {
	fake := &fakeMouse{}
	if err := Click(context.Background(), fake, false, DefaultConfig()); err != nil {
		t.Fatalf("Click returned error: %v", err)
	}
	if fake.downs != 1 || fake.ups != 1 {
		t.Errorf("Click downs=%d ups=%d; want 1,1", fake.downs, fake.ups)
	}
}

func TestClickTargetForInputBiasesLeft(t *testing.T) {
	cfg := DefaultConfig()
	box := Box{X: 100, Y: 100, Width: 200, Height: 30}
	for i := 0; i < 50; i++ {
		got := ClickTarget(box, true, cfg)
		// xFrac is in [0.05, 0.30] → x is in [110, 160]
		if got.X < 100+0.05*200-1 || got.X > 100+0.30*200+1 {
			t.Errorf("input click target x=%v outside biased range", got.X)
		}
	}
}

func TestClickTargetForButtonStaysCentered(t *testing.T) {
	cfg := DefaultConfig()
	box := Box{X: 0, Y: 0, Width: 100, Height: 40}
	for i := 0; i < 50; i++ {
		got := ClickTarget(box, false, cfg)
		// xFrac in [0.35, 0.65] → x in [35, 65]; allow ±1 rounding
		if got.X < 34 || got.X > 66 {
			t.Errorf("button click target x=%v outside centered range", got.X)
		}
	}
}
