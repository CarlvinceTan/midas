package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// MCPDescription is what a client sees in the server list.
const MCPDescription = "Local agent homeserver: installs messaging bridges on demand and exposes their accounts, messages, and logins. No bridges are attached until you install one."

// ServeStdio runs the hub's MCP server over stdin and stdout.
func (h *Hub) ServeStdio(ctx context.Context) error {
	return h.mcpServer().Run(ctx, &mcp.StdioTransport{})
}

// mcpServer returns the hub's one MCP server. Clients share it, which is what
// lets resource subscriptions and their notifications reach the right sessions.
func (h *Hub) mcpServer() *mcp.Server {
	h.mcpOnce.Do(func() { h.mcpInstance = h.server() })
	return h.mcpInstance
}

// MCPHandler returns an HTTP handler serving the hub's MCP endpoint, for clients
// that share one long-lived homeserver. Callers must authenticate requests
// themselves; the hub's own Listen does.
func (h *Hub) MCPHandler() *mcp.StreamableHTTPHandler {
	return mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return h.mcpServer() }, nil)
}

// server builds the MCP server. The tool list is fixed: installing a bridge never
// changes which tools exist, only what they return, so a client's request prefix
// stays cacheable across installs.
func (h *Hub) server() *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "hub", Version: "0.1.0"}, &mcp.ServerOptions{
		Instructions: MCPDescription,
		// Push needs the sessions a client has opened. The handshake may or may not
		// announce them, so every tool call also attaches its session; attaching
		// twice is a no-op.
		// Subscriptions are what make push work: a client that subscribes to a
		// thread resource is told when that thread changes.
		SubscribeHandler:   func(context.Context, *mcp.SubscribeRequest) error { return nil },
		UnsubscribeHandler: func(context.Context, *mcp.UnsubscribeRequest) error { return nil },
	})
	mcp.AddTool(server, &mcp.Tool{
		Name:        "hub_bridges",
		Description: "List installed bridges, list what the registry offers, and install, uninstall, start, or stop them. Nothing is installed by default; with no bridges, the other hub tools return empty results.",
	}, h.bridgesTool)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "hub_accounts",
		Description: "List the accounts the installed bridges serve.",
	}, h.accountsTool)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "hub_send",
		Description: "Send a message through a bridge account.",
	}, h.sendTool)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "hub_messages",
		Description: "Read recent messages for an account and optional thread. Returns a bounded window, never a whole history.",
	}, h.messagesTool)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "hub_threads",
		Description: "List conversations for an account with unread counts, newest activity first.",
	}, h.threadsTool)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "hub_mark_read",
		Description: "Mark a thread read so unread counts and other devices agree.",
	}, h.markReadTool)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "hub_login",
		Description: "Start, inspect, answer, or cancel an account login. Challenges are a QR payload, a code, a link, or a password prompt; pass render=true to get printable QR rows for a terminal.",
	}, h.loginTool)
	h.addResources(server)
	return server
}

// addResources exposes stored data as resources, so a client can read a thread or
// watch a login without spending a tool call on it.
func (h *Hub) addResources(server *mcp.Server) {
	server.AddResourceTemplate(&mcp.ResourceTemplate{
		URITemplate: "hub://threads/{account}/{thread}",
		Name:        "Thread",
		Description: "Messages in one conversation.",
		MIMEType:    "application/json",
	}, func(ctx context.Context, request *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		uri := request.Params.URI
		parts := strings.Split(strings.TrimPrefix(uri, "hub://threads/"), "/")
		if len(parts) != 2 {
			return nil, fmt.Errorf("hub: expected hub://threads/{account}/{thread}, got %q", uri)
		}
		// A thread id may contain reserved characters, so the segments are escaped
		// in the URI and unescaped here.
		account, accountErr := url.PathUnescape(parts[0])
		if accountErr != nil {
			account = parts[0]
		}
		thread, threadErr := url.PathUnescape(parts[1])
		if threadErr != nil {
			thread = parts[1]
		}
		messages, err := h.History(ctx, h.singleBridge(), account, thread, 50)
		if err != nil {
			return nil, err
		}
		encoded, err := json.Marshal(messages)
		if err != nil {
			return nil, err
		}
		return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{
			URI: uri, MIMEType: "application/json", Text: string(encoded),
		}}}, nil
	})
	server.AddResourceTemplate(&mcp.ResourceTemplate{
		URITemplate: "hub://logins/{loginID}",
		Name:        "Login",
		Description: "An in-flight account login and its current challenge.",
		MIMEType:    "application/json",
	}, func(_ context.Context, request *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		loginID := strings.TrimPrefix(request.Params.URI, "hub://logins/")
		login, err := h.LoginStatus(loginID, h.sessionKey(request.Session), false)
		if err != nil {
			return nil, err
		}
		encoded, err := json.Marshal(login)
		if err != nil {
			return nil, err
		}
		return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{
			URI: request.Params.URI, MIMEType: "application/json", Text: string(encoded),
		}}}, nil
	})
}

type bridgesInput struct {
	Action   string            `json:"action" jsonschema:"list, available, install, uninstall, start, or stop"`
	Name     string            `json:"name,omitempty" jsonschema:"bridge name"`
	Source   string            `json:"source,omitempty" jsonschema:"local path or URL to the bridge binary, required for install"`
	Checksum string            `json:"checksum,omitempty" jsonschema:"expected sha256 of the bridge binary"`
	Command  string            `json:"command,omitempty" jsonschema:"command that runs the bridge, required for install"`
	Env      map[string]string `json:"env,omitempty" jsonschema:"extra environment for the bridge process, such as a token path"`
	Purge    bool              `json:"purge,omitempty" jsonschema:"uninstall: also delete the bridge's files"`
	Refresh  bool              `json:"refresh,omitempty" jsonschema:"available: read the registry again instead of using the cached copy"`
}

type bridgesOutput struct {
	Bridges   []BridgeStatus `json:"bridges,omitempty"`
	Available []CatalogEntry `json:"available,omitempty"`
	Message   string         `json:"message,omitempty"`
}

func (h *Hub) bridgesTool(ctx context.Context, request *mcp.CallToolRequest, input bridgesInput) (*mcp.CallToolResult, bridgesOutput, error) {
	h.attachSession(requestSession(request))
	switch strings.ToLower(strings.TrimSpace(input.Action)) {
	case "", "list":
		return nil, bridgesOutput{Bridges: h.BridgeStatuses(), Message: emptyNotice(h)}, nil
	case "available":
		entries, err := h.Available(ctx, input.Refresh)
		if err != nil {
			return nil, bridgesOutput{}, err
		}
		if len(entries) == 0 {
			return nil, bridgesOutput{Message: "no registry is configured; install with an explicit source, or set registry in hub.json to a catalog path or URL"}, nil
		}
		return nil, bridgesOutput{Available: entries}, nil
	case "install":
		status, err := h.Install(ctx, InstallOptions{
			Name: input.Name, Source: input.Source, Checksum: input.Checksum,
			Command: strings.Fields(input.Command), Env: input.Env,
		})
		if err != nil {
			return nil, bridgesOutput{}, err
		}
		return nil, bridgesOutput{Bridges: []BridgeStatus{status}, Message: "installed; it starts on first use"}, nil
	case "uninstall":
		if err := h.Uninstall(input.Name, input.Purge); err != nil {
			return nil, bridgesOutput{}, err
		}
		return nil, bridgesOutput{Message: "uninstalled"}, nil
	case "start":
		if err := h.StartBridge(ctx, input.Name); err != nil {
			return nil, bridgesOutput{}, err
		}
		return nil, bridgesOutput{Message: "started"}, nil
	case "stop":
		if err := h.StopBridge(input.Name); err != nil {
			return nil, bridgesOutput{}, err
		}
		return nil, bridgesOutput{Message: "stopped"}, nil
	default:
		return nil, bridgesOutput{}, fmt.Errorf("unknown action %q", input.Action)
	}
}

type accountsOutput struct {
	Accounts []Account `json:"accounts"`
	Message  string    `json:"message,omitempty"`
}

func (h *Hub) accountsTool(ctx context.Context, request *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, accountsOutput, error) {
	h.attachSession(requestSession(request))
	if len(h.BridgeStatuses()) == 0 {
		return nil, accountsOutput{Accounts: []Account{}, Message: emptyNotice(h)}, nil
	}
	accounts, err := h.Accounts(ctx)
	if err != nil {
		// Listing accounts is how a client finds its way around, so a bridge that
		// cannot be reached leaves the answer empty with the reason in the message
		// rather than failing the call: an environment with nothing logged in is a
		// normal state, not an error.
		return nil, accountsOutput{Accounts: []Account{}, Message: err.Error()}, nil
	}
	return nil, accountsOutput{Accounts: accounts}, nil
}

// mediaInput is one attachment to send. The hub passes it to the bridge, which
// reads the bytes: a path on this machine, or a URL for a bridge that fetches.
type mediaInput struct {
	Kind       string `json:"kind,omitempty" jsonschema:"image, video, audio, voice, document, or sticker"`
	Path       string `json:"path,omitempty" jsonschema:"path to the file to send"`
	URL        string `json:"url,omitempty" jsonschema:"URL of the media, for a bridge that fetches it"`
	MIME       string `json:"mime,omitempty" jsonschema:"content type, e.g. image/png"`
	Filename   string `json:"filename,omitempty" jsonschema:"file name to send"`
	Caption    string `json:"caption,omitempty" jsonschema:"caption attached to the media"`
	DurationMS int64  `json:"durationMs,omitempty" jsonschema:"length of a voice note, video, or audio"`
}

type sendInput struct {
	Bridge  string `json:"bridge,omitempty" jsonschema:"bridge to send through; optional when only one is installed"`
	Account string `json:"account" jsonschema:"account ID"`
	To      string `json:"to" jsonschema:"thread or recipient"`
	Text    string `json:"text,omitempty" jsonschema:"message text; optional when media is sent"`
	ReplyTo string `json:"replyTo,omitempty" jsonschema:"id of the message this one answers"`
	// Media are the attachments. A message with no text and no media has nothing
	// to send and is rejected.
	Media []mediaInput `json:"media,omitempty" jsonschema:"attachments to send"`
}

func (h *Hub) sendTool(ctx context.Context, request *mcp.CallToolRequest, input sendInput) (*mcp.CallToolResult, Message, error) {
	h.attachSession(requestSession(request))
	if len(h.BridgeStatuses()) == 0 {
		return nil, Message{}, fmt.Errorf("%s", emptyNotice(h))
	}
	media := make([]Media, 0, len(input.Media))
	for _, attachment := range input.Media {
		media = append(media, Media{
			Kind: strings.TrimSpace(attachment.Kind), Path: strings.TrimSpace(attachment.Path),
			URL: strings.TrimSpace(attachment.URL), MIME: strings.TrimSpace(attachment.MIME),
			Filename: strings.TrimSpace(attachment.Filename), Caption: attachment.Caption,
			DurationMS: attachment.DurationMS,
		})
	}
	sent, err := h.Send(ctx, SendRequest{
		Bridge: input.Bridge, Account: input.Account, To: input.To,
		Text: input.Text, ReplyTo: strings.TrimSpace(input.ReplyTo), Media: media,
	})
	if err != nil {
		return nil, Message{}, err
	}
	return nil, sent, nil
}

type messagesInput struct {
	Bridge  string `json:"bridge,omitempty" jsonschema:"bridge to read from; optional when only one is installed"`
	Account string `json:"account,omitempty" jsonschema:"account ID"`
	Thread  string `json:"thread,omitempty" jsonschema:"thread to read"`
	Limit   int    `json:"limit,omitempty" jsonschema:"maximum messages to return, default 50"`
}

type messagesOutput struct {
	Messages []Message `json:"messages"`
	Message  string    `json:"message,omitempty"`
}

func (h *Hub) messagesTool(ctx context.Context, request *mcp.CallToolRequest, input messagesInput) (*mcp.CallToolResult, messagesOutput, error) {
	h.attachSession(requestSession(request))
	if len(h.BridgeStatuses()) == 0 {
		return nil, messagesOutput{Messages: []Message{}, Message: emptyNotice(h)}, nil
	}
	messages, err := h.History(ctx, input.Bridge, input.Account, input.Thread, input.Limit)
	if err != nil {
		return nil, messagesOutput{}, err
	}
	if messages == nil {
		messages = []Message{}
	}
	return nil, messagesOutput{Messages: messages}, nil
}

type threadsInput struct {
	Bridge  string `json:"bridge,omitempty" jsonschema:"bridge to read from; optional when only one is installed"`
	Account string `json:"account,omitempty" jsonschema:"account ID"`
}

type threadsOutput struct {
	Threads []Thread `json:"threads"`
	Message string   `json:"message,omitempty"`
}

func (h *Hub) threadsTool(ctx context.Context, request *mcp.CallToolRequest, input threadsInput) (*mcp.CallToolResult, threadsOutput, error) {
	h.attachSession(requestSession(request))
	if len(h.BridgeStatuses()) == 0 {
		return nil, threadsOutput{Threads: []Thread{}, Message: emptyNotice(h)}, nil
	}
	threads, err := h.Threads(ctx, input.Bridge, input.Account)
	if err != nil {
		return nil, threadsOutput{}, err
	}
	if threads == nil {
		threads = []Thread{}
	}
	return nil, threadsOutput{Threads: threads}, nil
}

type markReadInput struct {
	Bridge  string `json:"bridge,omitempty" jsonschema:"bridge to notify; optional when only one is installed"`
	Account string `json:"account" jsonschema:"account ID"`
	Thread  string `json:"thread" jsonschema:"thread to mark read"`
	UpTo    int64  `json:"upTo,omitempty" jsonschema:"unix milliseconds to mark up to, default now"`
}

func (h *Hub) markReadTool(ctx context.Context, request *mcp.CallToolRequest, input markReadInput) (*mcp.CallToolResult, Thread, error) {
	h.attachSession(requestSession(request))
	if len(h.BridgeStatuses()) == 0 {
		return nil, Thread{}, fmt.Errorf("%s", emptyNotice(h))
	}
	thread, err := h.MarkRead(ctx, input.Bridge, input.Account, input.Thread, input.UpTo)
	if err != nil {
		return nil, Thread{}, err
	}
	return nil, thread, nil
}

type loginInput struct {
	Action   string `json:"action" jsonschema:"start, status, submit, or cancel"`
	LoginID  string `json:"loginID,omitempty" jsonschema:"login to act on, from start or status"`
	Bridge   string `json:"bridge,omitempty" jsonschema:"bridge to log in to"`
	Account  string `json:"account,omitempty" jsonschema:"account ID, required for start"`
	Response string `json:"response,omitempty" jsonschema:"answer for a code or password challenge"`
	Render   bool   `json:"render,omitempty" jsonschema:"include printable QR rows for a terminal"`
}

type loginOutput struct {
	Logins  []Login `json:"logins,omitempty"`
	Message string  `json:"message,omitempty"`
}

func (h *Hub) loginTool(ctx context.Context, request *mcp.CallToolRequest, input loginInput) (*mcp.CallToolResult, loginOutput, error) {
	session := ""
	if request != nil {
		h.attachSession(request.Session)
		session = h.sessionKey(request.Session)
	}
	switch strings.ToLower(strings.TrimSpace(input.Action)) {
	case "start":
		if strings.TrimSpace(input.Account) == "" {
			return nil, loginOutput{}, fmt.Errorf("an account is required to start a login")
		}
		login, err := h.LoginStart(ctx, session, input.Bridge, input.Account)
		if err != nil {
			return nil, loginOutput{}, err
		}
		if input.Render && login.Challenge.Kind == "qr" {
			login.Rendered = RenderQR(login.Challenge.Payload)
		}
		return nil, loginOutput{Logins: []Login{login}}, nil
	case "status":
		if strings.TrimSpace(input.LoginID) == "" {
			return nil, loginOutput{Logins: h.logins.listOwned(session)}, nil
		}
		login, err := h.LoginStatus(input.LoginID, session, input.Render)
		if err != nil {
			return nil, loginOutput{}, err
		}
		return nil, loginOutput{Logins: []Login{login}}, nil
	case "submit":
		login, err := h.LoginSubmit(ctx, input.LoginID, session, input.Response)
		if err != nil {
			return nil, loginOutput{}, err
		}
		return nil, loginOutput{Logins: []Login{login}}, nil
	case "cancel":
		if err := h.LoginCancel(ctx, input.LoginID, session); err != nil {
			return nil, loginOutput{}, err
		}
		return nil, loginOutput{Message: "cancelled"}, nil
	default:
		return nil, loginOutput{}, fmt.Errorf("unknown action %q", input.Action)
	}
}

// requestSession is the session behind a tool call, when the transport provides
// one.
func requestSession(request *mcp.CallToolRequest) *mcp.ServerSession {
	if request == nil {
		return nil
	}
	return request.Session
}

// emptyNotice is the message every tool returns when nothing is installed, so a
// client is told what to do instead of seeing an empty result with no reason.
func emptyNotice(h *Hub) string {
	if len(h.BridgeStatuses()) > 0 {
		return ""
	}
	return "no bridges are installed; use hub_bridges with action install and a source path or URL"
}
