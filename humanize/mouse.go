package humanize

import (
	"context"
	"math"
	"math/rand/v2"
	"time"
)

// RawMouse is the low-level mouse surface humanize dispatches against. The
// adapter in midas/browser maps these onto Input.dispatchMouseEvent CDP
// commands; tests can supply a recording fake.
type RawMouse interface {
	Move(ctx context.Context, x, y float64) error
	Down(ctx context.Context) error
	Up(ctx context.Context) error
	Wheel(ctx context.Context, deltaX, deltaY float64) error
}

// Point is a 2D coordinate in CSS pixels.
type Point struct {
	X, Y float64
}

// Box is an element bounding box in CSS pixels.
type Box struct {
	X, Y, Width, Height float64
}

func easeInOut(t float64) float64 {
	if t < 0.5 {
		return 4 * t * t * t
	}
	return 1 - math.Pow(-2*t+2, 3)/2
}

func bezier(p0, p1, p2, p3 Point, t float64) Point {
	u := 1 - t
	uu := u * u
	uuu := uu * u
	tt := t * t
	ttt := tt * t
	return Point{
		X: uuu*p0.X + 3*uu*t*p1.X + 3*u*tt*p2.X + ttt*p3.X,
		Y: uuu*p0.Y + 3*uu*t*p1.Y + 3*u*tt*p2.Y + ttt*p3.Y,
	}
}

func randomControlPoints(start, end Point) (Point, Point) {
	dx := end.X - start.X
	dy := end.Y - start.Y
	dist := math.Hypot(dx, dy)
	if dist == 0 {
		dist = 1
	}
	// Unit vector perpendicular to the segment, used to bow the curve sideways.
	px := -dy / dist
	py := dx / dist
	bias1 := Rand(-0.3, 0.3) * dist
	bias2 := Rand(-0.3, 0.3) * dist
	return Point{
			X: start.X + dx*0.25 + px*bias1,
			Y: start.Y + dy*0.25 + py*bias1,
		}, Point{
			X: start.X + dx*0.75 + px*bias2,
			Y: start.Y + dy*0.75 + py*bias2,
		}
}

// Move dispatches a sequence of mouseMoved events tracing a randomized cubic
// Bezier curve from (startX, startY) to (endX, endY). Speed is ease-in-out;
// each step adds sine-modulated wobble; with cfg.MouseOvershootChance the
// final approach overshoots the target and corrects back.
func Move(ctx context.Context, raw RawMouse, startX, startY, endX, endY float64, cfg Config) error {
	dist := math.Hypot(endX-startX, endY-startY)
	if dist < 1 {
		return nil
	}

	steps := int(math.Round(dist / cfg.MouseStepsDivisor))
	if steps < cfg.MouseMinSteps {
		steps = cfg.MouseMinSteps
	}
	if steps > cfg.MouseMaxSteps {
		steps = cfg.MouseMaxSteps
	}

	start := Point{startX, startY}
	end := Point{endX, endY}
	cp1, cp2 := randomControlPoints(start, end)

	burstCounter := 0
	burstSize := RandIntRange(cfg.MouseBurstSize)

	for i := 0; i <= steps; i++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		progress := float64(i) / float64(steps)
		t := easeInOut(progress)
		pt := bezier(start, cp1, cp2, end, t)

		wobbleAmp := math.Sin(math.Pi*progress) * cfg.MouseWobbleMax
		wx := pt.X + (rand.Float64()-0.5)*2*wobbleAmp
		wy := pt.Y + (rand.Float64()-0.5)*2*wobbleAmp

		if err := raw.Move(ctx, math.Round(wx), math.Round(wy)); err != nil {
			return err
		}

		burstCounter++
		if burstCounter >= burstSize && i < steps {
			if err := SleepMs(ctx, RandRange(cfg.MouseBurstPause)); err != nil {
				return err
			}
			burstCounter = 0
			burstSize = RandIntRange(cfg.MouseBurstSize)
		}
	}

	if rand.Float64() < cfg.MouseOvershootChance {
		overshoot := RandRange(cfg.MouseOvershootPx)
		angle := math.Atan2(endY-startY, endX-startX)
		if err := raw.Move(ctx,
			math.Round(endX+math.Cos(angle)*overshoot),
			math.Round(endY+math.Sin(angle)*overshoot),
		); err != nil {
			return err
		}
		if err := SleepMs(ctx, Rand(30, 70)); err != nil {
			return err
		}
		if err := raw.Move(ctx,
			math.Round(endX+(rand.Float64()-0.5)*4),
			math.Round(endY+(rand.Float64()-0.5)*4),
		); err != nil {
			return err
		}
	}

	return nil
}

// ClickTarget picks a randomized point inside box. Inputs bias toward the left
// edge (so the cursor lands within the editable text area, not on trailing
// whitespace); buttons land near the center.
func ClickTarget(box Box, isInput bool, cfg Config) Point {
	var xFrac, yFrac float64
	if isInput {
		xFrac = RandRange(cfg.ClickInputXRange)
		yFrac = Rand(0.30, 0.70)
	} else {
		xFrac = Rand(0.35, 0.65)
		yFrac = Rand(0.35, 0.65)
	}
	return Point{
		X: math.Round(box.X + box.Width*xFrac),
		Y: math.Round(box.Y + box.Height*yFrac),
	}
}

// Click issues an aim-delay → mouseDown → hold → mouseUp sequence at the
// current cursor position. The caller is responsible for moving the cursor
// to the target via Move first.
func Click(ctx context.Context, raw RawMouse, isInput bool, cfg Config) error {
	var aim, hold float64
	if isInput {
		aim = RandRange(cfg.ClickAimDelayInput)
		hold = RandRange(cfg.ClickHoldInput)
	} else {
		aim = RandRange(cfg.ClickAimDelayButton)
		hold = RandRange(cfg.ClickHoldButton)
	}
	if err := SleepMs(ctx, aim); err != nil {
		return err
	}
	if err := raw.Down(ctx); err != nil {
		return err
	}
	if err := SleepMs(ctx, hold); err != nil {
		return err
	}
	return raw.Up(ctx)
}

// Idle dispatches small drifting mouse movements for the given duration to
// simulate a still-but-not-frozen cursor between actions.
func Idle(ctx context.Context, raw RawMouse, duration time.Duration, cx, cy float64, cfg Config) error {
	deadline := time.Now().Add(duration)
	x, y := cx, cy
	for time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			return err
		}
		x += (rand.Float64() - 0.5) * 2 * cfg.IdleDriftPx
		y += (rand.Float64() - 0.5) * 2 * cfg.IdleDriftPx
		if err := raw.Move(ctx, math.Round(x), math.Round(y)); err != nil {
			return err
		}
		if err := SleepMs(ctx, RandRange(cfg.IdlePauseRange)); err != nil {
			return err
		}
	}
	return nil
}
