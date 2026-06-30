package main

// rpc.go is the node RPC client: a thin HTTP wrapper around the same endpoints
// the cereblix-wallet CLI uses (GET /balance, /status, /history, /block, /tx,
// /mempool, /richlist, /search, /blocks; POST /tx). It is parameterized by the
// active node base URL so node_modes.go can drive Lite failover, Custom, or the
// loopback Full node through one code path.
//
// The node answers logical failures with a JSON envelope {"error": "..."} on an
// otherwise-200 response; a *down* node fails at the transport layer or with 5xx.
// We distinguish the two so Lite mode only fails over on real connectivity loss,
// not on a legitimate "bad address" style error.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// rpcClient performs node RPC over HTTP with a bounded timeout.
type rpcClient struct {
	http *http.Client
}

func newRPCClient() *rpcClient {
	return &rpcClient{http: &http.Client{Timeout: 20 * time.Second}}
}

// netError marks a transport-level / server-down failure (as opposed to a logical
// node error). Lite mode fails over to the next endpoint only on a netError.
type netError struct{ err error }

func (e *netError) Error() string { return e.err.Error() }
func (e *netError) Unwrap() error { return e.err }

func isNetError(err error) bool {
	var ne *netError
	return errors.As(err, &ne)
}

// get performs GET base+path and decodes the JSON body into out. A {"error":...}
// envelope becomes a plain error; a dead endpoint becomes a *netError.
func (c *rpcClient) get(base, path string, out any) error {
	resp, err := c.http.Get(strings.TrimRight(base, "/") + path)
	if err != nil {
		return &netError{err}
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 500 {
		return &netError{fmt.Errorf("node %s: HTTP %d", base, resp.StatusCode)}
	}
	if err := decodeEnvelope(body); err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("decode %s: %w", path, err)
	}
	return nil
}

// post performs POST base+path with a JSON body and decodes the reply into out.
func (c *rpcClient) post(base, path string, body, out any) error {
	raw, _ := json.Marshal(body)
	resp, err := c.http.Post(strings.TrimRight(base, "/")+path, "application/json", bytes.NewReader(raw))
	if err != nil {
		return &netError{err}
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 500 {
		return &netError{fmt.Errorf("node %s: HTTP %d", base, resp.StatusCode)}
	}
	if err := decodeEnvelope(data); err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("decode %s: %w", path, err)
	}
	return nil
}

// decodeEnvelope returns the node's {"error":"..."} message as a Go error, or nil.
func decodeEnvelope(body []byte) error {
	var probe map[string]json.RawMessage
	if json.Unmarshal(body, &probe) == nil {
		if e, ok := probe["error"]; ok {
			var msg string
			if json.Unmarshal(e, &msg) == nil && msg != "" {
				return errors.New(msg)
			}
			return errors.New("node returned an error")
		}
	}
	return nil
}
