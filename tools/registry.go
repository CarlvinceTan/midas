package tools

import (
	"context"
	"fmt"

	"github.com/carlvincetan/polymux/internal/midas/browser"
)

type Spec struct {
	Name        string
	Description string
	InputHint   string
}

type Result struct {
	Message string
	Value   any
}

type Handler func(context.Context, *browser.Context, map[string]any) (Result, error)

type Registry struct {
	specs map[string]registeredTool
	order []string
}

type registeredTool struct {
	Spec
	Handler Handler
}

func NewRegistry() Registry {
	r := Registry{
		specs: make(map[string]registeredTool),
	}
	r.register(Spec{
		Name:        "go_to",
		Description: "Navigate the active page to a URL.",
		InputHint:   `{"url":"https://example.com","wait_until":"load|domcontentloaded|networkidle"}`,
	}, goTo)
	r.register(Spec{
		Name:        "nav_back",
		Description: "Go back in page history.",
		InputHint:   `{"wait_until":"load|domcontentloaded|networkidle"}`,
	}, navBack)
	r.register(Spec{
		Name:        "click",
		Description: "Click an element by CSS selector.",
		InputHint:   `{"selector":"button.submit","double":false}`,
	}, click)
	r.register(Spec{
		Name:        "type",
		Description: "Type text into an element by CSS selector.",
		InputHint:   `{"selector":"input[name=q]","text":"hello","clear":true,"delay_ms":0}`,
	}, typeText)
	r.register(Spec{
		Name:        "fill_form",
		Description: "Fill multiple form fields by CSS selector.",
		InputHint:   `{"fields":[{"selector":"input[name=email]","value":"user@example.com"}]}`,
	}, fillForm)
	r.register(Spec{
		Name:        "scroll",
		Description: "Scroll the page or a scrollable element.",
		InputHint:   `{"selector":".results","percent":100} or {"delta_y":800}`,
	}, scroll)
	r.register(Spec{
		Name:        "drag_and_drop",
		Description: "Drag one element to another.",
		InputHint:   `{"from_selector":"[data-id=source]","to_selector":"[data-id=target]","steps":8}`,
	}, dragAndDrop)
	r.register(Spec{
		Name:        "click_and_hold",
		Description: "Click and hold an element for a duration.",
		InputHint:   `{"selector":"button.record","duration_ms":1000}`,
	}, clickAndHold)
	r.register(Spec{
		Name:        "keys",
		Description: "Press a keyboard key or combination.",
		InputHint:   `{"key":"Enter"}`,
	}, keys)
	r.register(Spec{
		Name:        "wait",
		Description: "Wait for time to pass or for a selector to appear.",
		InputHint:   `{"duration_ms":1000} or {"selector":"#done","timeout_ms":5000}`,
	}, waitTool)
	r.register(Spec{
		Name:        "think",
		Description: "Record reasoning without touching the browser.",
		InputHint:   `{"note":"what was learned and next step"}`,
	}, think)
	r.register(Spec{
		Name:        "extract",
		Description: "Extract structured page data or element text/value/html.",
		InputHint:   `{"selector":"h1","property":"text|html|value"} or {"property":"snapshot"}`,
	}, extract)
	r.register(Spec{
		Name:        "aria_tree",
		Description: "Get the accessibility tree for the current page.",
		InputHint:   `{"with_frames":true}`,
	}, ariaTree)
	r.register(Spec{
		Name:        "screenshot",
		Description: "Capture a screenshot of the page or an element.",
		InputHint:   `{"selector":"main","full_page":false}`,
	}, screenshot)
	return r
}

func (r *Registry) register(spec Spec, handler Handler) {
	r.specs[spec.Name] = registeredTool{Spec: spec, Handler: handler}
	r.order = append(r.order, spec.Name)
}

func (r Registry) Specs() []Spec {
	specs := make([]Spec, 0, len(r.order))
	for _, name := range r.order {
		specs = append(specs, r.specs[name].Spec)
	}
	return specs
}

func (r Registry) Execute(ctx context.Context, bctx *browser.Context, name string, input map[string]any) (Result, error) {
	spec, ok := r.specs[name]
	if !ok {
		return Result{}, fmt.Errorf("unknown tool %q", name)
	}
	return spec.Handler(ctx, bctx, input)
}
