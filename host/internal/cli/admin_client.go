package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// adminClient talks to a running host over its loopback admin API.
//
// Every subcommand that inspects or mutates a live host goes through this, never
// through the device store on disk: the running process owns that state, and
// reading the file behind its back would show stale data and could race a write.
type adminClient struct {
	base  string
	token string
	http  *http.Client
}

func newAdminClient(state runtimeState) *adminClient {
	host, port, err := net.SplitHostPort(state.Addr)
	if err != nil || host == "" {
		host = "127.0.0.1"
		port = fmt.Sprint(state.Port)
	}
	// The admin API is loopback-only by design, so the client always dials
	// loopback even if the host is bound to a LAN address.
	if !isLoopbackHost(host) {
		host = "127.0.0.1"
	}
	return &adminClient{
		base:  fmt.Sprintf("http://%s", net.JoinHostPort(host, port)),
		token: state.AdminToken,
		http:  &http.Client{Timeout: 10 * time.Second},
	}
}

func isLoopbackHost(h string) bool {
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// do performs an admin request and decodes the JSON response.
func (c *adminClient) do(method, path string, body any) (json.RawMessage, error) {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.base+path, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cannot reach the host at %s: %w", c.base, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, fmt.Errorf("reading the host's response: %w", err)
	}
	if resp.StatusCode >= 400 {
		var e struct {
			Error   string `json:"error"`
			Message string `json:"message"`
		}
		if json.Unmarshal(raw, &e) == nil && e.Message != "" {
			return nil, fmt.Errorf("%s (%s)", e.Message, e.Error)
		}
		return nil, fmt.Errorf("the host answered %s: %s", resp.Status, strings.TrimSpace(string(raw)))
	}
	return raw, nil
}

// get performs a GET and decodes into v.
func (c *adminClient) get(path string, v any) error {
	raw, err := c.do(http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	return decodeInto(raw, v)
}

// post performs a POST and decodes into v.
func (c *adminClient) post(path string, body, v any) error {
	raw, err := c.do(http.MethodPost, path, body)
	if err != nil {
		return err
	}
	if v == nil {
		return nil
	}
	return decodeInto(raw, v)
}

// patch performs a PATCH and decodes into v.
func (c *adminClient) patch(path string, body, v any) error {
	raw, err := c.do(http.MethodPatch, path, body)
	if err != nil {
		return err
	}
	if v == nil {
		return nil
	}
	return decodeInto(raw, v)
}

// put performs a PUT and decodes into v.
func (c *adminClient) put(path string, body, v any) error {
	raw, err := c.do(http.MethodPut, path, body)
	if err != nil {
		return err
	}
	if v == nil {
		return nil
	}
	return decodeInto(raw, v)
}

// delete performs a DELETE and decodes into v.
func (c *adminClient) delete(path string, v any) error {
	raw, err := c.do(http.MethodDelete, path, nil)
	if err != nil {
		return err
	}
	if v == nil {
		return nil
	}
	return decodeInto(raw, v)
}

func decodeInto(raw json.RawMessage, v any) error {
	if v == nil {
		return nil
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return fmt.Errorf("the host's response was not valid JSON: %w", err)
	}
	return nil
}

// errHostUnreachable wraps the "nothing is running" case so callers can produce
// the right message and exit code.
var errHostUnreachable = errors.New("no running host answered")

// connectRuntime reads the published runtime state and returns a client for it.
func connectRuntime(paths runtimePaths) (*adminClient, runtimeState, error) {
	state, err := readRuntime(paths.runtimeFile)
	if err != nil {
		if errors.Is(err, errNotRunning) {
			return nil, state, fmt.Errorf("%w: start it with `mobiledeck run` or `mobiledeck start`", errHostUnreachable)
		}
		return nil, state, err
	}
	// A stale runtime file from a crashed process would otherwise produce a
	// confusing connection error; check the pid first.
	if !processAlive(state.PID) {
		return nil, state, fmt.Errorf("%w: the recorded process (pid %d) is gone; remove %s if this persists",
			errHostUnreachable, state.PID, paths.runtimeFile)
	}
	return newAdminClient(state), state, nil
}

// runtimePaths bundles the paths the CLI needs to find a running host.
type runtimePaths struct {
	runtimeFile string
	pidFile     string
	logFile     string
	root        string
}
