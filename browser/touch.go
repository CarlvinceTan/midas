package browser

import (
	"context"
)

type TouchPoint struct {
	X             float64
	Y             float64
	RadiusX       float64
	RadiusY       float64
	RotationAngle float64
	Force         float64
}

type Touch struct {
	page *Page
}

func newTouch(page *Page) *Touch {
	return &Touch{page: page}
}

func (t *Touch) Tap(ctx context.Context, x, y float64) error {
	if err := t.TouchStart(ctx, TouchPoint{X: x, Y: y}); err != nil {
		return err
	}
	return t.TouchEnd(ctx)
}

func (t *Touch) TouchStart(ctx context.Context, points ...TouchPoint) error {
	touchPoints := make([]map[string]any, len(points))
	for i, p := range points {
		tp := map[string]any{
			"x": p.X,
			"y": p.Y,
		}
		if p.RadiusX > 0 {
			tp["radiusX"] = p.RadiusX
		}
		if p.RadiusY > 0 {
			tp["radiusY"] = p.RadiusY
		}
		if p.RotationAngle != 0 {
			tp["rotationAngle"] = p.RotationAngle
		}
		if p.Force > 0 {
			tp["force"] = p.Force
		}
		touchPoints[i] = tp
	}
	return t.page.mainSession.Send(ctx, "Input.dispatchTouchEvent", map[string]any{
		"type":        "touchStart",
		"touchPoints": touchPoints,
	}, nil)
}

func (t *Touch) TouchMove(ctx context.Context, points ...TouchPoint) error {
	touchPoints := make([]map[string]any, len(points))
	for i, p := range points {
		tp := map[string]any{
			"x": p.X,
			"y": p.Y,
		}
		if p.RadiusX > 0 {
			tp["radiusX"] = p.RadiusX
		}
		if p.RadiusY > 0 {
			tp["radiusY"] = p.RadiusY
		}
		if p.RotationAngle != 0 {
			tp["rotationAngle"] = p.RotationAngle
		}
		if p.Force > 0 {
			tp["force"] = p.Force
		}
		touchPoints[i] = tp
	}
	return t.page.mainSession.Send(ctx, "Input.dispatchTouchEvent", map[string]any{
		"type":        "touchMove",
		"touchPoints": touchPoints,
	}, nil)
}

func (t *Touch) TouchEnd(ctx context.Context) error {
	return t.page.mainSession.Send(ctx, "Input.dispatchTouchEvent", map[string]any{
		"type":        "touchEnd",
		"touchPoints": []map[string]any{},
	}, nil)
}

func (t *Touch) TouchCancel(ctx context.Context) error {
	return t.page.mainSession.Send(ctx, "Input.dispatchTouchEvent", map[string]any{
		"type":        "touchCancel",
		"touchPoints": []map[string]any{},
	}, nil)
}
