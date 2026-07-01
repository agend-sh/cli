package mcp

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"log"

	"github.com/agend-sh/cli/internal/api"
	"github.com/agend-sh/cli/internal/auth"
	agentgrpc "github.com/agend-sh/cli/internal/grpc"
	"github.com/agend-sh/cli/internal/recovery"
	pb "github.com/agend-sh/cli/proto/agentd/v1"
)

// JSON-RPC 2.0 types

type jsonrpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type jsonrpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type Server struct {
	pool    *ConnPool
	api     *api.Client
	version string
	scanner *bufio.Scanner
	writer  io.Writer
	mu      sync.Mutex // guards writer

	inflightMu sync.Mutex
	inflight   map[string]context.CancelFunc // in-flight tools/call by request id
}

func NewServer(apiClient *api.Client, version string) *Server {
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	return &Server{
		pool:     NewConnPool(apiClient),
		api:      apiClient,
		version:  version,
		scanner:  scanner,
		writer:   os.Stdout,
		inflight: make(map[string]context.CancelFunc),
	}
}

// maxCallDuration is a generous backstop so a wedged tool call can't leak a
// goroutine forever. Interactive execs have their own (shorter) daemon-side
// timeout; this only catches a truly stuck connection.
const maxCallDuration = 10 * time.Minute

func (s *Server) Run(ctx context.Context) error {
	defer s.pool.CloseAll()

	for s.scanner.Scan() {
		line := s.scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var req jsonrpcRequest
		if err := json.Unmarshal(line, &req); err != nil {
			s.sendError(nil, -32700, "parse error")
			continue
		}

		// Dispatch tool calls concurrently so one slow shell_exec doesn't
		// freeze the whole interface — shell_interrupt / notifications/cancelled
		// must be able to run while a command is in flight. The writer is
		// mutex-guarded, so concurrent responses are safe. Lightweight methods
		// (initialize, tools/list, notifications) run inline to keep the
		// handshake ordered.
		if req.Method == "tools/call" {
			r := req // copy for the goroutine
			go s.dispatchCall(ctx, &r)
		} else {
			s.handleRequest(ctx, &req)
		}
	}
	return s.scanner.Err()
}

// dispatchCall runs a tools/call on its own cancelable, time-bounded context,
// registered by request id so notifications/cancelled can stop it.
func (s *Server) dispatchCall(ctx context.Context, req *jsonrpcRequest) {
	callCtx, cancel := context.WithTimeout(ctx, maxCallDuration)
	defer cancel()

	id := string(req.ID)
	if id != "" && id != "null" {
		s.inflightMu.Lock()
		s.inflight[id] = cancel
		s.inflightMu.Unlock()
		defer func() {
			s.inflightMu.Lock()
			delete(s.inflight, id)
			s.inflightMu.Unlock()
		}()
	}
	s.handleRequest(callCtx, req)
}

func (s *Server) handleRequest(ctx context.Context, req *jsonrpcRequest) {
	switch req.Method {
	case "initialize":
		// Echo the client's requested protocol version when it sends one
		// (negotiation), falling back to a version we support.
		protocolVersion := "2024-11-05"
		var initParams struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		if json.Unmarshal(req.Params, &initParams) == nil && initParams.ProtocolVersion != "" {
			protocolVersion = initParams.ProtocolVersion
		}
		s.sendResult(req.ID, map[string]any{
			"protocolVersion": protocolVersion,
			"capabilities": map[string]any{
				"tools": map[string]any{},
			},
			"serverInfo": map[string]any{
				"name":    "agend",
				"version": s.version,
			},
		})

	case "notifications/initialized":
		// no response

	case "notifications/cancelled":
		// Client asked to cancel an in-flight request — cancel its context so
		// the underlying gRPC call unwinds. No response (it's a notification).
		var p struct {
			RequestID json.RawMessage `json:"requestId"`
		}
		if json.Unmarshal(req.Params, &p) == nil && len(p.RequestID) > 0 {
			s.inflightMu.Lock()
			if cancel, ok := s.inflight[string(p.RequestID)]; ok {
				cancel()
			}
			s.inflightMu.Unlock()
		}

	case "tools/list":
		s.sendResult(req.ID, map[string]any{
			"tools": toolDefinitions(),
		})

	case "tools/call":
		var params struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &params); err != nil {
			s.sendError(req.ID, -32602, "invalid params")
			return
		}

		result, isErr := s.callTool(ctx, params.Name, params.Arguments)
		s.sendResult(req.ID, map[string]any{
			"content": []map[string]any{
				{"type": "text", "text": result},
			},
			"isError": isErr,
		})

	case "ping":
		s.sendResult(req.ID, map[string]any{})

	default:
		s.sendError(req.ID, -32601, fmt.Sprintf("method not found: %s", req.Method))
	}
}

// callTool dispatches a tool call, routing to the correct environment connection.
func (s *Server) callTool(ctx context.Context, name string, args map[string]any) (string, bool) {
	// API-only tools (no gRPC connection needed)
	switch name {
	case "list_environments":
		return s.listEnvironments()
	case "env_create":
		return s.envCreate()
	case "env_status":
		return s.envStatus(strArg(args, "environment"))
	case "env_wake":
		return s.envWake(strArg(args, "environment"))
	case "reload_config":
		return s.reloadConfig()
	}

	// Resolve domain credentials for port_expose with custom domains
	if name == "port_expose" {
		if domain := strArg(args, "domain"); domain != "" {
			parts := strings.Split(domain, ".")
			if len(parts) >= 2 {
				// Try progressively shorter zones to handle multi-part TLDs
				// e.g., app.example.com.br → try example.com.br, then com.br
				var resolved bool
				for i := 1; i < len(parts)-1; i++ {
					zone := strings.Join(parts[i:], ".")
					if zone == "agend.sh" {
						resolved = true // use infra credentials from MMDS
						break
					}
					creds, err := s.api.ResolveDomainCredentials(zone)
					if err == nil {
						args["cf_token"] = creds.CFToken
						args["cf_zone_id"] = creds.CFZoneID
						args["cf_account_id"] = creds.CFAccountID
						resolved = true
						break
					}
				}
				if !resolved {
					zone := strings.Join(parts[1:], ".")
					return fmt.Sprintf("domain lookup failed for zone %s: not registered\nRegister with: agend domain add %s --cf-token YOUR_TOKEN", zone, zone), true
				}
			}
		}
	}

	// All other tools require environment (accepts env ID or alias)
	envRef := strArg(args, "environment")
	if envRef == "" {
		return "environment is required — call list_environments first to get available environment IDs", true
	}

	// Resolve alias → env ID if needed
	envID := s.resolveEnvID(envRef)

	conn := s.pool.Get(envID)

	return conn.Execute(ctx, recovery.IsIdempotent(name), func(client *agentgrpc.Client) (string, bool) {
		return dispatchTool(ctx, client, name, args)
	})
}

// resolveEnvID resolves an alias or env ID to the actual env ID.
// If the input looks like an env ID (starts with "env-"), it's returned as-is.
// Otherwise, it's treated as an alias and looked up via the API.
func (s *Server) resolveEnvID(ref string) string {
	if len(ref) > 4 && ref[:4] == "env-" {
		return ref
	}

	// Look up alias
	resp, err := s.api.ListEnvironments()
	if err != nil {
		return ref // best effort — let it fail at connection time
	}
	for _, env := range resp.Environments {
		if env.Alias == ref {
			return env.EnvID
		}
	}
	return ref // not found — pass through, will error with "not found"
}

// controlPlaneErr converts a control-plane API failure into an actionable MCP
// message. The common case after the session JWT lapses is a 401 — guide the
// user to re-authenticate instead of surfacing a bare "api error 401", which
// reads to the agent as an opaque connection failure. `agend login` refreshes
// the token on disk; reload_config makes this running MCP server pick it up
// (it re-reads credentials + resets connections), so no restart is needed.
func controlPlaneErr(action string, err error) string {
	var apiErr *api.APIError
	if errors.As(err, &apiErr) && apiErr.StatusCode == 401 {
		return "Your agend session has expired or you're not signed in. " +
			"To reconnect: run `agend login` in a terminal, then call the reload_config tool and retry."
	}
	return fmt.Sprintf("%s failed: %v", action, err)
}

func (s *Server) listEnvironments() (string, bool) {
	resp, err := s.api.ListEnvironments()
	if err != nil {
		return controlPlaneErr("list environments", err), true
	}

	if len(resp.Environments) == 0 {
		return "No environments. Use 'agend env create' to create one.", false
	}

	var result string
	for _, env := range resp.Environments {
		name := env.EnvID
		if env.Alias != "" {
			name = fmt.Sprintf("%s (%s)", env.EnvID, env.Alias)
		}
		result += fmt.Sprintf("%s  state=%s  tier=%s\n", name, env.State, env.Tier)
	}
	return result, false
}

func (s *Server) envCreate() (string, bool) {
	resp, err := s.api.CreateEnvironment()
	if err != nil {
		return controlPlaneErr("create environment", err), true
	}
	// Store credentials so the connection pool can authenticate,
	// and persist to disk so MCP restarts can recover them.
	if resp.Secret != "" {
		conn := s.pool.Get(resp.EnvID)
		conn.SetSecret(resp.Secret)
	}
	auth.SaveEnvironment(resp.EnvID, resp.Endpoint, resp.Secret)
	return fmt.Sprintf("env_id: %s\nstate: %s\nendpoint: %s\n"+
		"note: a freshly-created tunnel can take up to ~60s to start routing. "+
		"The first shell_exec may need a few seconds — the connection auto-retries; "+
		"if it reports unreachable, wait a moment and try again.",
		resp.EnvID, resp.State, resp.Endpoint), false
}

func (s *Server) envStatus(envRef string) (string, bool) {
	if envRef == "" {
		return "environment is required", true
	}
	envID := s.resolveEnvID(envRef)
	resp, err := s.api.GetEnvironment(envID)
	if err != nil {
		return controlPlaneErr("environment status", err), true
	}
	return fmt.Sprintf("env_id: %s\nstate: %s\ntier: %s\nendpoint: %s\ncreated: %s\nlast_active: %s",
		resp.EnvID, resp.State, resp.Tier, resp.Endpoint, resp.CreatedAt, resp.LastActive), false
}

func (s *Server) envWake(envRef string) (string, bool) {
	if envRef == "" {
		return "environment is required", true
	}
	envID := s.resolveEnvID(envRef)
	resp, err := s.api.WakeEnvironment(envID)
	if err != nil {
		return controlPlaneErr("wake environment", err), true
	}
	// Store credentials so the connection pool can authenticate,
	// and persist to disk so MCP restarts can recover them.
	if resp.Secret != "" {
		conn := s.pool.Get(envID)
		conn.SetSecret(resp.Secret)
	}
	auth.SaveEnvironment(envID, resp.Endpoint, resp.Secret)
	return fmt.Sprintf("env_id: %s\nstate: %s\nendpoint: %s", resp.EnvID, resp.State, resp.Endpoint), false
}

func (s *Server) reloadConfig() (string, bool) {
	token, err := auth.LoadToken()
	if err != nil {
		return fmt.Sprintf("reload failed: no credentials on disk — run 'agend login' or 'agend signup' first: %v", err), true
	}

	newAPI := api.New(auth.LoadAPIURL(), token)

	s.mu.Lock()
	s.api = newAPI
	s.pool.Reset(newAPI)
	s.mu.Unlock()

	log.Printf("reload_config: credentials reloaded, connections reset")
	return "Config reloaded. API token and all connections reset.", false
}

// dispatchTool routes to the appropriate gRPC call.
func dispatchTool(ctx context.Context, client *agentgrpc.Client, name string, args map[string]any) (string, bool) {
	switch name {
	case "shell_exec":
		return callExec(ctx, client, args)
	case "shell_provide_input":
		return callInput(ctx, client, args)
	case "shell_send_raw":
		return callRawInput(ctx, client, args)
	case "shell_interrupt":
		return callInterrupt(ctx, client)
	case "shell_task_output":
		return callTaskOutput(ctx, client, args)
	case "shell_task_stop":
		return callTaskStop(ctx, client, args)
	case "file_download":
		return callFileDownload(ctx, client, args)
	case "file_upload":
		return callFileUpload(ctx, client, args)
	case "file_write":
		return callFileWrite(ctx, client, args)
	case "file_move":
		return callFileMove(ctx, client, args)
	case "env_stats":
		return callEnvStats(ctx, client)
	case "port_expose":
		return callPortExpose(ctx, client, args)
	case "port_unexpose":
		return callPortUnexpose(ctx, client, args)
	case "port_list":
		return callPortList(ctx, client)
	default:
		return fmt.Sprintf("unknown tool: %s", name), true
	}
}

func callExec(ctx context.Context, client *agentgrpc.Client, args map[string]any) (string, bool) {
	req := &pb.ExecRequest{
		Command: strArg(args, "command"),
	}
	if v, ok := args["timeout_ms"]; ok {
		req.TimeoutMs = uint32(numArg(v))
	}
	if v, ok := args["interactive"]; ok {
		req.Interactive, _ = v.(bool)
	}
	if v, ok := args["run_in_background"]; ok {
		req.RunInBackground, _ = v.(bool)
	}
	if req.Interactive && req.RunInBackground {
		return "interactive and run_in_background are mutually exclusive", true
	}
	if v, ok := args["tail_lines"]; ok {
		req.TailLines = uint32(numArg(v))
	}
	if v, ok := args["head_lines"]; ok {
		req.HeadLines = uint32(numArg(v))
	}

	// Client-side deadline: 4x server timeout for safety margin.
	// Accounts for WebSocket tunnel RTT, Cloudflare edge latency,
	// and retransmits. Without this, gRPC blocks indefinitely if the tunnel stalls.
	serverTimeout := time.Duration(req.TimeoutMs) * time.Millisecond
	if serverTimeout == 0 {
		serverTimeout = 30 * time.Second
	}
	callCtx, cancel := context.WithTimeout(ctx, serverTimeout*4)
	defer cancel()

	resp, err := client.Agent.Exec(callCtx, req)
	if err != nil {
		return fmt.Sprintf("exec failed: %v", err), true
	}
	return formatExecResponse(resp), false
}

func callInput(ctx context.Context, client *agentgrpc.Client, args map[string]any) (string, bool) {
	callCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	resp, err := client.Agent.Input(callCtx, &pb.InputRequest{
		Input: strArg(args, "input"),
	})
	if err != nil {
		return fmt.Sprintf("input failed: %v", err), true
	}

	out := fmt.Sprintf("status: %s", resp.Status)
	if resp.Stdout != "" {
		out += "\n" + resp.Stdout
	}
	if resp.Stderr != "" {
		out += "\nstderr: " + resp.Stderr
	}
	if resp.Status == "completed" {
		out += fmt.Sprintf("\nexit_code: %d", resp.ExitCode)
	}
	if resp.PromptType != "" {
		out += "\nprompt_type: " + resp.PromptType
	}
	return out, false
}

func callRawInput(ctx context.Context, client *agentgrpc.Client, args map[string]any) (string, bool) {
	callCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	resp, err := client.Agent.RawInput(callCtx, &pb.RawInputRequest{
		Input: strArg(args, "input"),
	})
	if err != nil {
		return fmt.Sprintf("raw_input failed: %v", err), true
	}

	out := fmt.Sprintf("status: %s", resp.Status)
	if resp.Screen != "" {
		out += "\n" + resp.Screen
	}
	if resp.Status == "completed" {
		out += fmt.Sprintf("\nexit_code: %d", resp.ExitCode)
	}
	return out, false
}

func callInterrupt(ctx context.Context, client *agentgrpc.Client) (string, bool) {
	resp, err := client.Agent.Interrupt(ctx, &pb.InterruptRequest{})
	if err != nil {
		return fmt.Sprintf("interrupt failed: %v", err), true
	}
	return fmt.Sprintf("status: %s", resp.Status), false
}

func callTaskOutput(ctx context.Context, client *agentgrpc.Client, args map[string]any) (string, bool) {
	resp, err := client.Agent.TaskOutput(ctx, &pb.TaskOutputRequest{
		TaskId: strArg(args, "task_id"),
	})
	if err != nil {
		return fmt.Sprintf("task_output failed: %v", err), true
	}

	out := fmt.Sprintf("status: %s", resp.Status)
	if resp.Stdout != "" {
		out += "\n" + resp.Stdout
	}
	if resp.Stderr != "" {
		out += "\nstderr: " + resp.Stderr
	}
	if resp.Status == "completed" {
		out += fmt.Sprintf("\nexit_code: %d", resp.ExitCode)
	}
	return out, false
}

func callTaskStop(ctx context.Context, client *agentgrpc.Client, args map[string]any) (string, bool) {
	_, err := client.Agent.TaskStop(ctx, &pb.TaskStopRequest{
		TaskId: strArg(args, "task_id"),
	})
	if err != nil {
		return fmt.Sprintf("task_stop failed: %v", err), true
	}
	return "task stopped", false
}

// callFileDownload downloads a file from the remote environment to a local path.
// Uses chunked transfer internally. No file content enters the LLM context.
func callFileDownload(ctx context.Context, client *agentgrpc.Client, args map[string]any) (string, bool) {
	remotePath := strArg(args, "remote_path")

	localPath, err := resolveLocalPath(strArg(args, "local_path"))
	if err != nil {
		return fmt.Sprintf("download failed: %v", err), true
	}

	const chunkSize = 1024 * 1024 // 1MB

	// First chunk — get total size and file checksum
	resp, err := client.Agent.FileGetChunked(ctx, &pb.FileGetChunkedRequest{
		Path:      remotePath,
		Offset:    0,
		ChunkSize: int32(chunkSize),
	})
	if err != nil {
		return fmt.Sprintf("download failed: %v", err), true
	}

	f, err := createLocalFile(localPath)
	if err != nil {
		return fmt.Sprintf("create local file: %v", err), true
	}
	defer f.Close()

	hasher := sha256.New()
	out := io.MultiWriter(f, hasher)

	if _, err := out.Write(resp.Data); err != nil {
		return fmt.Sprintf("write: %v", err), true
	}

	totalSize := resp.TotalSize
	downloaded := int64(resp.Size)
	fileChecksum := resp.FileChecksum

	for downloaded < totalSize {
		chunk, err := client.Agent.FileGetChunked(ctx, &pb.FileGetChunkedRequest{
			Path:      remotePath,
			Offset:    downloaded,
			ChunkSize: int32(chunkSize),
		})
		if err != nil {
			return fmt.Sprintf("download at offset %d: %v", downloaded, err), true
		}
		if _, err := out.Write(chunk.Data); err != nil {
			return fmt.Sprintf("write: %v", err), true
		}
		downloaded += int64(chunk.Size)
	}

	// Verify the received bytes against the checksum the daemon reported
	// for the whole file, so a corrupted or torn transfer can't be
	// reported as a success.
	if fileChecksum != "" {
		actual := hex.EncodeToString(hasher.Sum(nil))
		if actual != strings.ToLower(fileChecksum) {
			f.Close()
			os.Remove(localPath)
			return fmt.Sprintf("download failed: checksum mismatch (expected %s, got %s) — file removed, the remote file may have changed mid-transfer; retry", fileChecksum, actual), true
		}
	}

	return fmt.Sprintf("downloaded %s → %s (%d bytes, sha256: %s)", remotePath, localPath, totalSize, fileChecksum), false
}

// callFileUpload uploads a local file to the remote environment.
// Uses chunked transfer internally. Atomic write on the remote side.
func callFileUpload(ctx context.Context, client *agentgrpc.Client, args map[string]any) (string, bool) {
	remotePath := strArg(args, "remote_path")

	localPath, err := resolveLocalPath(strArg(args, "local_path"))
	if err != nil {
		return fmt.Sprintf("upload failed: %v", err), true
	}

	data, err := os.ReadFile(localPath)
	if err != nil {
		return fmt.Sprintf("read local file: %v", err), true
	}

	totalSize := int64(len(data))
	const chunkSize = 12 * 1024 // 12KB — Cloudflare tunnels drop gRPC messages >~16KB (HTTP/2 MAX_FRAME_SIZE)
	mode := strArg(args, "mode")
	createDirs := false
	if v, ok := args["create_dirs"]; ok {
		createDirs, _ = v.(bool)
	}

	var lastResp *pb.FilePutChunkedResponse
	for offset := int64(0); offset < totalSize; offset += int64(chunkSize) {
		end := offset + int64(chunkSize)
		if end > totalSize {
			end = totalSize
		}

		req := &pb.FilePutChunkedRequest{
			Path:      remotePath,
			Data:      data[offset:end],
			Offset:    offset,
			TotalSize: totalSize,
		}
		if offset == 0 {
			req.Mode = mode
			req.CreateDirs = createDirs
		}

		lastResp, err = client.Agent.FilePutChunked(ctx, req)
		if err != nil {
			return fmt.Sprintf("upload at offset %d: %v", offset, err), true
		}
	}

	result := fmt.Sprintf("uploaded %s → %s (%d bytes)", localPath, remotePath, totalSize)
	if lastResp != nil && lastResp.Checksum != "" {
		result += fmt.Sprintf(", sha256: %s", lastResp.Checksum)
	}
	return result, false
}

func callFileWrite(ctx context.Context, client *agentgrpc.Client, args map[string]any) (string, bool) {
	req := &pb.FilePutRequest{
		Path:    strArg(args, "path"),
		Content: strArg(args, "content"),
	}
	if v := strArg(args, "mode"); v != "" {
		req.Mode = v
	}
	if v, ok := args["create_dirs"]; ok {
		req.CreateDirs, _ = v.(bool)
	}
	req.Overwrite = true

	resp, err := client.Agent.FilePut(ctx, req)
	if err != nil {
		return fmt.Sprintf("file_write failed: %v", err), true
	}
	return fmt.Sprintf("written %s (%d bytes, sha256: %s)", strArg(args, "path"), resp.Size, resp.Checksum), false
}

func callEnvStats(ctx context.Context, client *agentgrpc.Client) (string, bool) {
	resp, err := client.Agent.Exec(ctx, &pb.ExecRequest{
		Command:   `echo "=== disk ===" && df -h / /data /opt/agentd /var/log/agentd 2>/dev/null | grep -v "^Filesystem" && echo "=== memory ===" && free -h | grep -E "Mem|Swap" && echo "=== cpu ===" && uptime && echo "=== processes ===" && ps aux --sort=-%mem | head -10`,
		TimeoutMs: 5000,
	})
	if err != nil {
		return fmt.Sprintf("stats failed: %v", err), true
	}
	return resp.Stdout, false
}

func callFileMove(ctx context.Context, client *agentgrpc.Client, args map[string]any) (string, bool) {
	_, err := client.Agent.FileMove(ctx, &pb.FileMoveRequest{
		Source:      strArg(args, "source"),
		Destination: strArg(args, "destination"),
	})
	if err != nil {
		return fmt.Sprintf("file_move failed: %v", err), true
	}
	return fmt.Sprintf("moved %s -> %s", strArg(args, "source"), strArg(args, "destination")), false
}

func callPortExpose(ctx context.Context, client *agentgrpc.Client, args map[string]any) (string, bool) {
	port := uint32(numArg(args["port"]))
	if port == 0 {
		return "port is required (1-65535)", true
	}

	callCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	req := &pb.PortExposeRequest{Port: port}
	if domain := strArg(args, "domain"); domain != "" {
		req.Domain = domain
	}
	if v := strArg(args, "cf_token"); v != "" {
		req.CfToken = v
	}
	if v := strArg(args, "cf_zone_id"); v != "" {
		req.CfZoneId = v
	}
	if v := strArg(args, "cf_account_id"); v != "" {
		req.CfAccountId = v
	}

	resp, err := client.Agent.PortExpose(callCtx, req)
	if err != nil {
		return fmt.Sprintf("port_expose failed: %v", err), true
	}

	if resp.Domain != "" {
		return fmt.Sprintf("port %d exposed: %s (domain: %s)", resp.Port, resp.Url, resp.Domain), false
	}
	return fmt.Sprintf("port %d exposed: %s", resp.Port, resp.Url), false
}

func callPortUnexpose(ctx context.Context, client *agentgrpc.Client, args map[string]any) (string, bool) {
	port := uint32(numArg(args["port"]))
	if port == 0 {
		return "port is required", true
	}
	domain, _ := args["domain"].(string)

	resp, err := client.Agent.PortUnexpose(ctx, &pb.PortUnexposeRequest{
		Port:   port,
		Domain: domain,
	})
	if err != nil {
		return fmt.Sprintf("port_unexpose failed: %v", err), true
	}
	_ = resp
	if domain != "" {
		return fmt.Sprintf("domain %s unbound from port %d", domain, port), false
	}
	return fmt.Sprintf("port %d unexposed", port), false
}

func callPortList(ctx context.Context, client *agentgrpc.Client) (string, bool) {
	resp, err := client.Agent.PortList(ctx, &pb.PortListRequest{})
	if err != nil {
		return fmt.Sprintf("port_list failed: %v", err), true
	}

	if len(resp.Ports) == 0 {
		return "no ports exposed", false
	}

	var result string
	for _, p := range resp.Ports {
		if p.Domain != "" {
			result += fmt.Sprintf("port %d: %s (domain: %s)\n", p.Port, p.Url, p.Domain)
		} else {
			result += fmt.Sprintf("port %d: %s\n", p.Port, p.Url)
		}
	}
	return result, false
}

func formatExecResponse(resp *pb.ExecResponse) string {
	if resp.TaskId != "" {
		return fmt.Sprintf("task_id: %s", resp.TaskId)
	}

	out := fmt.Sprintf("status: %s", resp.Status)
	if resp.Stdout != "" {
		out += "\n" + resp.Stdout
	}
	if resp.Stderr != "" {
		out += "\nstderr: " + resp.Stderr
	}
	if resp.Screen != "" {
		out += "\n" + resp.Screen
	}
	if resp.Status == "completed" {
		out += fmt.Sprintf("\nexit_code: %d", resp.ExitCode)
	}
	if resp.PromptType != "" {
		out += "\nprompt_type: " + resp.PromptType
	}
	if resp.Truncated {
		out += fmt.Sprintf("\ntruncated: showing %d of %d lines", resp.ShownLines, resp.TotalLines)
	}
	return out
}

func strArg(args map[string]any, key string) string {
	v, _ := args[key].(string)
	return v
}

func numArg(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case json.Number:
		f, _ := n.Float64()
		return f
	case string:
		// LLMs frequently pass numeric args as strings (e.g. port "8080").
		// Parse them instead of silently coercing to 0.
		f, err := strconv.ParseFloat(strings.TrimSpace(n), 64)
		if err != nil {
			return 0
		}
		return f
	default:
		return 0
	}
}

func (s *Server) sendResult(id json.RawMessage, result any) {
	s.send(jsonrpcResponse{JSONRPC: "2.0", ID: id, Result: result})
}

func (s *Server) sendError(id json.RawMessage, code int, message string) {
	s.send(jsonrpcResponse{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: message}})
}

func (s *Server) send(resp jsonrpcResponse) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := json.Marshal(resp)
	if err != nil {
		fmt.Fprintf(s.writer, `{"jsonrpc":"2.0","id":null,"error":{"code":-32603,"message":"marshal error"}}`+"\n")
		return
	}
	fmt.Fprintf(s.writer, "%s\n", data)
}
