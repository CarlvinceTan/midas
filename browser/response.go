package browser

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"sync"
)

type Response struct {
	page       *Page
	session    sessionLike
	RequestID  string
	FrameID    string
	LoaderID   string
	URLValue   string
	StatusCode int

	statusText        string
	headers           map[string]string
	headerValues      map[string][]string
	headersText       string
	headersArray      []HeaderEntry
	extraHeaders      map[string]string
	extraHeadersText  string
	mimeType          string
	remoteIPAddress   string
	remotePort        int
	fromServiceWorker bool
	securityDetails   map[string]any

	mu         sync.Mutex
	finished   bool
	finishErr  error
	finishWait chan struct{}

	// lifetimeUnsubs are CDP listeners installed for this response's requestID
	// so late-arriving Network.responseReceivedExtraInfo / loadingFinished /
	// loadingFailed events still update the response after the navigation call
	// returns. Cleared and invoked from markFinished — see also Dispose for
	// callers that abandon a response without waiting for it to finish.
	lifetimeUnsubs []func()
}

func newResponse(page *Page, session sessionLike, requestID, frameID, loaderID string, details *networkResponseDetails) *Response {
	resp := &Response{
		page:       page,
		session:    session,
		RequestID:  requestID,
		FrameID:    frameID,
		LoaderID:   loaderID,
		finishWait: make(chan struct{}),
	}
	resp.applyDetails(details)
	resp.installLifetimeListeners()
	return resp
}

// installLifetimeListeners subscribes to the per-request CDP events that can
// arrive after the navigation tracker has handed the Response back to the
// caller — extra-info headers (set-cookie, security headers) and the
// loadingFinished / loadingFailed signal that closes finishWait. Unsubscribes
// happen in markFinished so the listeners self-clean once the request is done.
func (r *Response) installLifetimeListeners() {
	if r.session == nil || r.RequestID == "" {
		return
	}
	requestID := r.RequestID
	unsubs := []func(){
		r.session.On("Network.responseReceivedExtraInfo", func(params json.RawMessage) {
			var evt struct {
				RequestID   string         `json:"requestId"`
				Headers     map[string]any `json:"headers"`
				HeadersText string         `json:"headersText"`
			}
			if json.Unmarshal(params, &evt) != nil || evt.RequestID != requestID {
				return
			}
			r.applyExtraInfo(stringifyMap(evt.Headers), evt.HeadersText)
		}),
		r.session.On("Network.loadingFinished", func(params json.RawMessage) {
			var evt struct {
				RequestID string `json:"requestId"`
			}
			if json.Unmarshal(params, &evt) != nil || evt.RequestID != requestID {
				return
			}
			r.markFinished(nil)
		}),
		r.session.On("Network.loadingFailed", func(params json.RawMessage) {
			var evt struct {
				RequestID string `json:"requestId"`
				ErrorText string `json:"errorText"`
			}
			if json.Unmarshal(params, &evt) != nil || evt.RequestID != requestID {
				return
			}
			r.markFinished(errors.New(defaultString(evt.ErrorText, "Navigation request failed")))
		}),
	}
	r.mu.Lock()
	r.lifetimeUnsubs = unsubs
	r.mu.Unlock()
}

// Dispose detaches the response's CDP listeners without marking it finished.
// Use when abandoning a response that will never complete (e.g. the page is
// being torn down). Safe to call multiple times.
func (r *Response) Dispose() {
	r.mu.Lock()
	unsubs := r.lifetimeUnsubs
	r.lifetimeUnsubs = nil
	r.mu.Unlock()
	for _, unsub := range unsubs {
		if unsub != nil {
			unsub()
		}
	}
}

func (r *Response) applyDetails(details *networkResponseDetails) {
	if details == nil {
		return
	}
	r.URLValue = details.URL
	r.StatusCode = details.Status
	r.statusText = details.StatusText
	r.mimeType = details.MimeType
	r.remoteIPAddress = details.RemoteIPAddress
	r.remotePort = details.RemotePort
	r.fromServiceWorker = details.FromServiceWorker
	r.securityDetails = cloneJSON(details.SecurityDetails)
	r.headers = normalizeHeaderMap(details.Headers)
	r.headerValues = splitHeaderMap(details.Headers)
	r.headersText = details.HeadersText
	r.extraHeaders = normalizeHeaderMap(details.ExtraHeaders)
	r.extraHeadersText = details.ExtraHeadersText
	r.headersArray = parseHeadersText(details.HeadersText, details.Headers)
	if len(r.headersArray) == 0 {
		r.headersArray = headersMapToArray(details.Headers)
	}
}

func (r *Response) applyExtraInfo(headers map[string]string, headersText string) {
	if len(headers) == 0 && headersText == "" {
		return
	}
	// applyExtraInfo runs on the CDP event-handler goroutine (see
	// NavigationResponseTracker) after the response has already been published
	// to callers, so writes to extraHeaders / extraHeadersText / headersArray
	// race with HeaderValue / AllHeaders reads from the caller goroutine.
	// All header-state accessors take r.mu; this writer must too.
	r.mu.Lock()
	defer r.mu.Unlock()
	r.extraHeaders = mergeHeaders(r.extraHeaders, headers)
	if headersText != "" {
		r.extraHeadersText = headersText
		extraArray := parseHeadersText(headersText, headers)
		r.headersArray = mergeHeadersArrays(r.headersArray, extraArray)
	} else {
		for name, value := range headers {
			r.headersArray = appendHeaderIfMissing(r.headersArray, name, value)
		}
	}
}

func (r *Response) markFinished(err error) {
	r.mu.Lock()
	if r.finished {
		r.mu.Unlock()
		return
	}
	r.finished = true
	r.finishErr = err
	close(r.finishWait)
	unsubs := r.lifetimeUnsubs
	r.lifetimeUnsubs = nil
	r.mu.Unlock()
	for _, unsub := range unsubs {
		if unsub != nil {
			unsub()
		}
	}
}

func (r *Response) Finished() error {
	<-r.finishWait
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.finishErr
}

func (r *Response) URL() string {
	return r.URLValue
}

func (r *Response) Status() int {
	return r.StatusCode
}

func (r *Response) StatusText() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.statusText
}

func (r *Response) Ok() bool {
	return r.StatusCode >= 200 && r.StatusCode <= 299
}

func (r *Response) Err() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.finishErr
}

func (r *Response) Frame() *Frame {
	if r.page == nil || r.FrameID == "" {
		return nil
	}
	return r.page.Frame(r.FrameID)
}

func (r *Response) Headers() map[string]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return cloneStringMap(r.headers)
}

func (r *Response) AllHeaders() map[string]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	merged := make(map[string]string)
	for k, v := range r.headers {
		merged[strings.ToLower(k)] = v
	}
	for k, v := range r.extraHeaders {
		merged[strings.ToLower(k)] = v
	}
	if len(merged) == 0 {
		return nil
	}
	return merged
}

func (r *Response) HeaderValue(name string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	values := r.headerValuesLocked(name)
	if len(values) == 0 {
		return ""
	}
	return strings.Join(values, ", ")
}

func (r *Response) HeaderValues(name string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.headerValuesLocked(name)
}

// headerValuesLocked is the unlocked body shared by HeaderValue and
// HeaderValues. Callers MUST hold r.mu.
func (r *Response) headerValuesLocked(name string) []string {
	lowerName := strings.ToLower(name)
	result := r.headerValuesFromText(lowerName)
	if len(result) > 0 {
		return result
	}
	return r.headerValuesFromMap(lowerName)
}

func (r *Response) headerValuesFromText(lowerName string) []string {
	headersText := r.headersText
	extraText := r.extraHeadersText
	if headersText == "" && extraText == "" {
		return nil
	}
	var values []string
	if extraText != "" {
		values = append(values, extractHeaderValuesFromText(extraText, lowerName)...)
	}
	if headersText != "" {
		values = append(values, extractHeaderValuesFromText(headersText, lowerName)...)
	}
	return values
}

func (r *Response) headerValuesFromMap(lowerName string) []string {
	var values []string
	if vs, ok := r.headerValues[lowerName]; ok && len(vs) > 0 {
		values = append(values, vs...)
	}
	if v, ok := r.extraHeaders[lowerName]; ok {
		values = append(values, v)
	}
	if len(values) == 0 {
		return nil
	}
	out := make([]string, len(values))
	copy(out, values)
	return out
}

func (r *Response) HeadersArray() []HeaderEntry {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.headersArrayLocked()
}

// headersArrayLocked is the unlocked body shared by HeadersArray and
// Serializable. Callers MUST hold r.mu.
func (r *Response) headersArrayLocked() []HeaderEntry {
	if len(r.headersArray) == 0 {
		return nil
	}
	out := make([]HeaderEntry, len(r.headersArray))
	copy(out, r.headersArray)
	return out
}

func (r *Response) Body() ([]byte, error) {
	if r.session == nil {
		return nil, errors.New("response body unavailable")
	}
	var res struct {
		Body          string `json:"body"`
		Base64Encoded bool   `json:"base64Encoded"`
	}
	if err := r.session.Send(context.Background(), "Network.getResponseBody", map[string]any{
		"requestId": r.RequestID,
	}, &res); err != nil {
		return nil, err
	}
	if res.Base64Encoded {
		return base64.StdEncoding.DecodeString(res.Body)
	}
	return []byte(res.Body), nil
}

func (r *Response) Text() (string, error) {
	body, err := r.Body()
	if err != nil {
		return "", err
	}
	return string(body), nil
}

func (r *Response) JSON(result any) error {
	body, err := r.Body()
	if err != nil {
		return err
	}
	return json.Unmarshal(body, result)
}

func (r *Response) FromServiceWorker() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.fromServiceWorker
}

func (r *Response) ServerAddress() string {
	info := r.ServerAddressInfo()
	if info.IPAddress == "" {
		return ""
	}
	if info.Port <= 0 {
		return info.IPAddress
	}
	return info.IPAddress + ":" + itoa(info.Port)
}

func (r *Response) ServerAddressInfo() ServerAddressInfo {
	r.mu.Lock()
	defer r.mu.Unlock()
	return ServerAddressInfo{
		IPAddress: r.remoteIPAddress,
		Port:      r.remotePort,
	}
}

func (r *Response) SecurityDetails() map[string]any {
	r.mu.Lock()
	defer r.mu.Unlock()
	return cloneJSON(r.securityDetails)
}

func (r *Response) MIMEType() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.mimeType
}

func (r *Response) Serializable() SerializableResponse {
	r.mu.Lock()
	defer r.mu.Unlock()
	return SerializableResponse{
		RequestID:         r.RequestID,
		FrameID:           r.FrameID,
		LoaderID:          r.LoaderID,
		URL:               r.URLValue,
		Status:            r.StatusCode,
		StatusText:        r.statusText,
		Headers:           cloneStringMap(r.headers),
		HeadersText:       r.headersText,
		MIMEType:          r.mimeType,
		RemoteIPAddress:   r.remoteIPAddress,
		RemotePort:        r.remotePort,
		FromServiceWorker: r.fromServiceWorker,
		SecurityDetails:   cloneJSON(r.securityDetails),
		ExtraHeaders:      cloneStringMap(r.extraHeaders),
		ExtraHeadersText:  r.extraHeadersText,
		HeadersArray:      r.headersArrayLocked(),
	}
}

func NewResponseFromSerializable(page *Page, session sessionLike, sr SerializableResponse) *Response {
	return &Response{
		page:              page,
		session:           session,
		RequestID:         sr.RequestID,
		FrameID:           sr.FrameID,
		LoaderID:          sr.LoaderID,
		URLValue:          sr.URL,
		StatusCode:        sr.Status,
		statusText:        sr.StatusText,
		headers:           sr.Headers,
		headerValues:      splitHeaderMap(sr.Headers),
		headersText:       sr.HeadersText,
		mimeType:          sr.MIMEType,
		remoteIPAddress:   sr.RemoteIPAddress,
		remotePort:        sr.RemotePort,
		fromServiceWorker: sr.FromServiceWorker,
		securityDetails:   sr.SecurityDetails,
		extraHeaders:      sr.ExtraHeaders,
		extraHeadersText:  sr.ExtraHeadersText,
		headersArray:      sr.HeadersArray,
		finishWait:        make(chan struct{}),
	}
}

func normalizeHeaderMap(headers map[string]string) map[string]string {
	if len(headers) == 0 {
		return nil
	}
	out := make(map[string]string, len(headers))
	for key, value := range headers {
		out[strings.ToLower(key)] = value
	}
	return out
}

func mergeHeaders(base, overlay map[string]string) map[string]string {
	if base == nil && overlay == nil {
		return nil
	}
	out := make(map[string]string)
	for k, v := range base {
		out[strings.ToLower(k)] = v
	}
	for k, v := range overlay {
		out[strings.ToLower(k)] = v
	}
	return out
}

func parseHeadersText(headersText string, headers map[string]string) []HeaderEntry {
	if headersText == "" {
		return nil
	}
	lines := strings.Split(headersText, "\n")
	var entries []HeaderEntry
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		colonIdx := strings.Index(line, ":")
		if colonIdx <= 0 {
			continue
		}
		name := strings.TrimSpace(line[:colonIdx])
		value := strings.TrimSpace(line[colonIdx+1:])
		if name == "" {
			continue
		}
		entries = append(entries, HeaderEntry{Name: name, Value: value})
	}
	return entries
}

func headersMapToArray(headers map[string]string) []HeaderEntry {
	if len(headers) == 0 {
		return nil
	}
	entries := make([]HeaderEntry, 0, len(headers))
	for k, v := range headers {
		entries = append(entries, HeaderEntry{Name: k, Value: v})
	}
	return entries
}

func mergeHeadersArrays(base, overlay []HeaderEntry) []HeaderEntry {
	result := make([]HeaderEntry, len(base))
	copy(result, base)
	for _, entry := range overlay {
		result = appendHeaderIfMissing(result, entry.Name, entry.Value)
	}
	return result
}

func appendHeaderIfMissing(entries []HeaderEntry, name, value string) []HeaderEntry {
	lowerName := strings.ToLower(name)
	for _, e := range entries {
		if strings.ToLower(e.Name) == lowerName {
			return entries
		}
	}
	return append(entries, HeaderEntry{Name: name, Value: value})
}

func extractHeaderValuesFromText(headersText, lowerName string) []string {
	lines := strings.Split(headersText, "\n")
	var values []string
	for _, line := range lines {
		colonIdx := strings.Index(line, ":")
		if colonIdx <= 0 {
			continue
		}
		name := strings.TrimSpace(line[:colonIdx])
		if strings.ToLower(name) == lowerName {
			value := strings.TrimSpace(line[colonIdx+1:])
			if value != "" {
				values = append(values, value)
			}
		}
	}
	return values
}

func splitHeaderMap(headers map[string]string) map[string][]string {
	if len(headers) == 0 {
		return nil
	}
	out := make(map[string][]string, len(headers))
	for key, value := range headers {
		out[strings.ToLower(key)] = splitHeaderValue(value)
	}
	return out
}

func splitHeaderValue(value string) []string {
	if value == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	if len(out) == 0 {
		return []string{value}
	}
	return out
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

var errNoNavigationResponse = errors.New("no navigation response")
