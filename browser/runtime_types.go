package browser

import (
	"context"
	"encoding/json"
	"time"

	snappkg "github.com/carlvincetan/polymux/internal/midas/browser/snapshot"
	"github.com/carlvincetan/polymux/internal/midas/cdp"
)

type LoadState string

const (
	LoadStateDOMContentLoaded LoadState = "domcontentloaded"
	LoadStateLoad             LoadState = "load"
	LoadStateNetworkIdle      LoadState = "networkidle"
)

type ConnectOptions struct {
	Headers                    map[string]string
	UserAgent                  string
	EnsureFirstTopLevelPage    bool
	FirstTopLevelPageTimeoutMs int
}

type Cookie struct {
	Name     string
	Value    string
	Domain   string
	Path     string
	Expires  float64
	HTTPOnly bool
	Secure   bool
	SameSite string
	URL      string
}

type CookieFilter struct {
	Name   string
	Domain string
	Path   string
}

type ClearCookieOptions struct {
	Name   string
	Domain string
	Path   string
}

type SelectorState string

const (
	SelectorStateAttached SelectorState = "attached"
	SelectorStateDetached SelectorState = "detached"
	SelectorStateVisible  SelectorState = "visible"
	SelectorStateHidden   SelectorState = "hidden"
)

type WaitForSelectorOptions struct {
	State        SelectorState
	Timeout      time.Duration
	PierceShadow bool
}

type ConsoleMessage struct {
	Type       string
	Text       string
	Args       []map[string]any
	Location   map[string]any
	Timestamp  float64
	PageTarget string
}

type ConsoleListener func(ConsoleMessage)

type ScreenshotScaleMode string

const (
	ScreenshotScaleCSS    ScreenshotScaleMode = "css"
	ScreenshotScaleDevice ScreenshotScaleMode = "device"
)

type ScreenshotAnimationsMode string

const (
	ScreenshotAnimationsDisabled ScreenshotAnimationsMode = "disabled"
	ScreenshotAnimationsAllow    ScreenshotAnimationsMode = "allow"
)

type ScreenshotCaretMode string

const (
	ScreenshotCaretHide    ScreenshotCaretMode = "hide"
	ScreenshotCaretInitial ScreenshotCaretMode = "initial"
)

type ScreenshotOptions struct {
	Format            string
	Quality           int
	FullPage          bool
	OmitBackground    bool
	DisableAnimations bool
	HideCaret         bool
	Clip              *ScreenshotClip
	TransparentBg     bool
	Scale             ScreenshotScaleMode
	Style             string
	Mask              []*Locator
	MaskColor         string
	Animations        ScreenshotAnimationsMode
	Caret             ScreenshotCaretMode
	WaitBeforeCapture time.Duration
}

type ScreenshotClip struct {
	X      float64
	Y      float64
	Width  float64
	Height float64
	Scale  float64
}

type SnapshotResult = snappkg.Result
type SnapshotOptions = snappkg.Options
type PerFrameSnapshot = snappkg.PerFrame
type ResolvedLocation = snappkg.ResolvedLocation

type FilePayload struct {
	Name         string
	MIMEType     string
	Buffer       []byte
	LastModified int64
}

type sessionLike interface {
	ID() string
	Send(ctx context.Context, method string, params any, result any) error
	On(event string, handler cdp.EventHandler) cdp.Unsubscribe
	Close(ctx context.Context) error
}

type networkRequestInfo struct {
	sessionID       string
	requestID       string
	requestKey      string
	frameID         string
	loaderID        string
	url             string
	timestamp       time.Time
	resourceType    string
	documentRequest bool
	response        *networkResponseDetails
	failureText     string
}

type networkResponseDetails struct {
	URL               string
	Status            int
	StatusText        string
	Headers           map[string]string
	HeadersText       string
	MimeType          string
	RemoteIPAddress   string
	RemotePort        int
	FromServiceWorker bool
	SecurityDetails   map[string]any
	ExtraHeaders      map[string]string
	ExtraHeadersText  string
}

type HeaderEntry struct {
	Name  string
	Value string
}

type ServerAddressInfo struct {
	IPAddress string
	Port      int
}

type SecurityDetailsInfo struct {
	SubjectName    string   `json:"subjectName,omitempty"`
	Issuer         string   `json:"issuer,omitempty"`
	ValidFrom      float64  `json:"validFrom,omitempty"`
	ValidTo        float64  `json:"validTo,omitempty"`
	CertificateID  string   `json:"certificateId,omitempty"`
	SubjectTo      string   `json:"subjectTo,omitempty"`
	SanList        []string `json:"sanList,omitempty"`
	CertListSize   int      `json:"certListSize,omitempty"`
	SignedCertList []string `json:"signedCertList,omitempty"`
}

type SerializableResponse struct {
	RequestID         string            `json:"requestId"`
	FrameID           string            `json:"frameId"`
	LoaderID          string            `json:"loaderId"`
	URL               string            `json:"url"`
	Status            int               `json:"status"`
	StatusText        string            `json:"statusText"`
	Headers           map[string]string `json:"headers,omitempty"`
	HeadersText       string            `json:"headersText,omitempty"`
	MIMEType          string            `json:"mimeType,omitempty"`
	RemoteIPAddress   string            `json:"remoteIPAddress,omitempty"`
	RemotePort        int               `json:"remotePort,omitempty"`
	FromServiceWorker bool              `json:"fromServiceWorker"`
	SecurityDetails   map[string]any    `json:"securityDetails,omitempty"`
	ExtraHeaders      map[string]string `json:"extraHeaders,omitempty"`
	ExtraHeadersText  string            `json:"extraHeadersText,omitempty"`
	HeadersArray      []HeaderEntry     `json:"headersArray,omitempty"`
}

type networkObserver struct {
	onRequestStarted  func(info networkRequestInfo)
	onRequestFinished func(info networkRequestInfo)
	onRequestFailed   func(info networkRequestInfo)
}

type waitForIdleOptions struct {
	startTime   time.Time
	timeout     time.Duration
	totalBudget time.Duration
	idleTime    time.Duration
	filter      func(info networkRequestInfo) bool
}

type waitForIdleHandle struct {
	promise chan error
	dispose func()
}

type connLike interface {
	Send(ctx context.Context, method string, params any, result any) error
	On(event string, handler cdp.EventHandler) cdp.Unsubscribe
	Close() error
	EnableAutoAttach(ctx context.Context) error
	AttachToTarget(ctx context.Context, targetID string) (sessionLike, error)
	GetSession(sessionID string) (sessionLike, bool)
	GetTargets(ctx context.Context) ([]cdp.TargetInfo, error)
}

type pageNavigateResult struct {
	FrameID   string `json:"frameId,omitempty"`
	LoaderID  string `json:"loaderId,omitempty"`
	ErrorText string `json:"errorText,omitempty"`
}

type frameNode struct {
	Frame       cdpFrame    `json:"frame"`
	ChildFrames []frameNode `json:"childFrames,omitempty"`
}

type cdpFrame struct {
	ID       string `json:"id"`
	ParentID string `json:"parentId,omitempty"`
	LoaderID string `json:"loaderId,omitempty"`
	URL      string `json:"url,omitempty"`
	Name     string `json:"name,omitempty"`
}

type frameAttachedEvent struct {
	FrameID       string `json:"frameId"`
	ParentFrameID string `json:"parentFrameId,omitempty"`
}

type frameDetachedEvent struct {
	FrameID string `json:"frameId"`
	Reason  string `json:"reason,omitempty"`
}

type frameNavigatedEvent struct {
	Frame cdpFrame `json:"frame"`
}

type navigatedWithinDocumentEvent struct {
	FrameID string `json:"frameId"`
	URL     string `json:"url"`
}

type lifecycleEvent struct {
	FrameID string `json:"frameId"`
	Name    string `json:"name"`
}

func cloneJSON[T any](v T) T {
	var zero T
	buf, err := json.Marshal(v)
	if err != nil {
		return zero
	}
	if err := json.Unmarshal(buf, &zero); err != nil {
		var fallback T
		return fallback
	}
	return zero
}
