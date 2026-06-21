// Package argus is midas's client for the argus semantic-UI-graph server
// (github's polymux/argus). When MIDAS_ARGUS_URL is set, Snapshot() captures
// the page (screenshot + AX tree + DOMSnapshot), POSTs it to the argus server's
// /parse endpoint, and renders the returned UIGraph into the same FormattedTree
// shape midas's heuristic snapshot produces — so the agent prompt is a semantic
// graph instead of a raw a11y outline. Empty MIDAS_ARGUS_URL = feature off
// (zero behaviour change); any error falls back to the heuristic snapshot.
//
// Wire protocol mirrors argus/serve/server.py (ParseRequest/UIGraph) and the
// Python reference client argus/serve/adapters/midas.py exactly. See
// argus/INTEGRATION.md.
package argus

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"time"
)

const defaultTimeout = 10 * time.Second

// BBox is a viewport-relative box in CSS pixels (matches argus BBox: x,y,w,h).
type BBox struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
	W float64 `json:"w"`
	H float64 `json:"h"`
}

// NodeInput is one ParseRequest node (argus serve/server.py::NodeInput, plus the
// prompt-only input_value the server ignores but build_argus_text renders).
type NodeInput struct {
	EncodedID  string            `json:"encoded_id"`
	Tag        string            `json:"tag"`
	Text       string            `json:"text"`
	Attrs      map[string]string `json:"attrs"`
	AriaRole   string            `json:"aria_role"`
	ParentID   *string           `json:"parent_id"`
	ChildIDs   []string          `json:"child_ids"`
	BBox       *BBox             `json:"bbox"`
	XPath      string            `json:"xpath"`
	InputValue string            `json:"input_value,omitempty"`
	// Computed-style hints (prompt-only; the server ignores extras). cursor is
	// near-ground-truth clickability; display/visibility let the renderer drop
	// hidden overlays. Populated from DOMSnapshot's computedStyles.
	Cursor     string `json:"cursor,omitempty"`
	Display    string `json:"display,omitempty"`
	Visibility string `json:"visibility,omitempty"`
	Opacity    string `json:"opacity,omitempty"`
}

// ParseRequest is the POST /parse body (argus serve/server.py::ParseRequest).
type ParseRequest struct {
	PageID           string      `json:"page_id"`
	ScreenshotB64    string      `json:"screenshot_b64"`
	ViewportWidth    int         `json:"viewport_width"`
	ViewportHeight   int         `json:"viewport_height"`
	ScreenshotWidth  int         `json:"screenshot_width"`
	ScreenshotHeight int         `json:"screenshot_height"`
	Nodes            []NodeInput `json:"nodes"`
}

// UINode is one classified node in the UIGraph response.
type UINode struct {
	ID          string   `json:"id"`
	Type        string   `json:"type"`
	Role        string   `json:"role"`
	Label       string   `json:"label"`
	BBox        *BBox    `json:"bbox"`
	Confidence  *float64 `json:"confidence"`
	XPath       string   `json:"xpath"`
	CSSSelector string   `json:"css_selector"`
	// Interactability head (roi arch): predicted affordance the AX tree + type
	// filter miss. Surfaced into the prompt so DOM-invisible clickables reach
	// the agent. Defaults (unknown/false/0) for pernode/legacy checkpoints.
	Interact      string  `json:"interact"`
	Interactable  bool    `json:"interactable"`
	InteractScore float64 `json:"interact_score"`
}

// UIEdge is one parent→children structural edge.
type UIEdge struct {
	Parent   string   `json:"parent"`
	Children []string `json:"children"`
}

// UIGraph is the /parse response (argus data/schema.py::UIGraph).
type UIGraph struct {
	PageID    string         `json:"page_id"`
	PageType  string         `json:"page_type"`
	Nodes     []UINode       `json:"nodes"`
	Structure []UIEdge       `json:"structure"`
	Metadata  map[string]any `json:"metadata"`
}

// Client talks to one argus server. The zero value / a nil *Client is "disabled"
// — callers gate on Enabled().
type Client struct {
	BaseURL string
	HTTP    *http.Client
}

// FromEnv builds a Client from MIDAS_ARGUS_URL (+ MIDAS_ARGUS_TIMEOUT_MS).
// Empty MIDAS_ARGUS_URL returns a disabled client (Enabled() == false), so the
// argus path never runs unless explicitly configured.
func FromEnv() *Client {
	base := os.Getenv("MIDAS_ARGUS_URL")
	timeout := defaultTimeout
	if v := os.Getenv("MIDAS_ARGUS_TIMEOUT_MS"); v != "" {
		if ms, err := strconv.Atoi(v); err == nil && ms > 0 {
			timeout = time.Duration(ms) * time.Millisecond
		}
	}
	return New(base, timeout)
}

// New builds a Client for baseURL with the given HTTP timeout. Empty baseURL =
// disabled.
func New(baseURL string, timeout time.Duration) *Client {
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	// Trim a trailing slash so {base}/parse never double-slashes.
	for len(baseURL) > 0 && baseURL[len(baseURL)-1] == '/' {
		baseURL = baseURL[:len(baseURL)-1]
	}
	return &Client{BaseURL: baseURL, HTTP: &http.Client{Timeout: timeout}}
}

// Enabled reports whether an argus server URL is configured.
func (c *Client) Enabled() bool {
	return c != nil && c.BaseURL != ""
}

// Health probes GET /health; true only when the server answers {"ok": true}.
// Used to skip argus for a whole session when the server is down (one cheap
// probe instead of a per-snapshot timeout).
func (c *Client) Health(ctx context.Context) bool {
	if !c.Enabled() {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/health", nil)
	if err != nil {
		return false
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	var out struct {
		OK bool `json:"ok"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return false
	}
	return out.OK
}

// parse POSTs a ParseRequest to /parse and decodes the UIGraph. Any transport,
// non-200, or decode error is returned so the caller falls back to the
// heuristic snapshot — never retried in the hot path.
func (c *Client) parse(ctx context.Context, req ParseRequest) (*UIGraph, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("argus: marshal request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/parse", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("argus: POST /parse: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("argus: /parse status %d", resp.StatusCode)
	}
	var graph UIGraph
	if err := json.NewDecoder(resp.Body).Decode(&graph); err != nil {
		return nil, fmt.Errorf("argus: decode UIGraph: %w", err)
	}
	return &graph, nil
}
