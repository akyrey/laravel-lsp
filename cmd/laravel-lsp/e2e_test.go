package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// The end-to-end test builds the real binary and drives it over stdio the
// way an editor does: initialize → initialized → didOpen → requests. It
// guards against the failure mode where every unit test passes but the
// editor gets nothing back (models extending vendor classes, #[Scope]
// scopes, closures, traits, migration columns, builder chains).

const (
	e2eFixture       = "../../testdata/laravel-app"
	e2eRequestWait   = 5 * time.Second
	e2eIndexDeadline = 10 * time.Second
	e2ePollInterval  = 50 * time.Millisecond
)

// lspClient is a minimal JSON-RPC 2.0 client over the server's stdio.
type lspClient struct {
	t      *testing.T
	stdin  io.WriteCloser
	nextID int

	mu        sync.Mutex
	responses map[int]chan json.RawMessage
	notes     []map[string]any
}

type rpcMessage struct {
	ID     *int            `json:"id"`
	Method string          `json:"method"`
	Result json.RawMessage `json:"result"`
	Params json.RawMessage `json:"params"`
}

func startServer(t *testing.T) *lspClient {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "laravel-lsp")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	cmd := exec.Command(bin)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = stdin.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})

	c := &lspClient{t: t, stdin: stdin, responses: make(map[int]chan json.RawMessage)}
	go c.readLoop(bufio.NewReader(stdout))
	return c
}

func (c *lspClient) readLoop(r *bufio.Reader) {
	for {
		length := 0
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			line = strings.TrimSpace(line)
			if line == "" {
				break
			}
			if v, ok := strings.CutPrefix(line, "Content-Length: "); ok {
				length, _ = strconv.Atoi(v)
			}
		}
		body := make([]byte, length)
		if _, err := io.ReadFull(r, body); err != nil {
			return
		}
		var msg rpcMessage
		if err := json.Unmarshal(body, &msg); err != nil {
			continue
		}
		c.mu.Lock()
		if msg.ID != nil && msg.Method == "" {
			if ch, ok := c.responses[*msg.ID]; ok {
				ch <- msg.Result
			}
		} else if msg.Method != "" {
			var note map[string]any
			_ = json.Unmarshal(body, &note)
			c.notes = append(c.notes, note)
		}
		c.mu.Unlock()
	}
}

func (c *lspClient) send(msg map[string]any) {
	c.t.Helper()
	msg["jsonrpc"] = "2.0"
	body, err := json.Marshal(msg)
	if err != nil {
		c.t.Fatal(err)
	}
	if _, err := fmt.Fprintf(c.stdin, "Content-Length: %d\r\n\r\n%s", len(body), body); err != nil {
		c.t.Fatal(err)
	}
}

func (c *lspClient) request(method string, params any) json.RawMessage {
	c.t.Helper()
	c.mu.Lock()
	c.nextID++
	id := c.nextID
	ch := make(chan json.RawMessage, 1)
	c.responses[id] = ch
	c.mu.Unlock()

	c.send(map[string]any{"id": id, "method": method, "params": params})
	select {
	case res := <-ch:
		return res
	case <-time.After(e2eRequestWait):
		c.t.Fatalf("%s: no response", method)
		return nil
	}
}

func (c *lspClient) notify(method string, params any) {
	c.send(map[string]any{"method": method, "params": params})
}

func fileURI(path string) string {
	return (&url.URL{Scheme: "file", Path: path}).String()
}

// position returns the LSP position of the first occurrence of needle in
// src, shifted by delta bytes (ASCII fixtures only).
func position(t *testing.T, src, needle string, delta int) map[string]int {
	t.Helper()
	i := strings.Index(src, needle)
	if i < 0 {
		t.Fatalf("%q not in fixture", needle)
	}
	off := i + delta
	return map[string]int{
		"line":      strings.Count(src[:off], "\n"),
		"character": off - strings.LastIndex(src[:off], "\n") - 1,
	}
}

type location struct {
	URI   string `json:"uri"`
	Range struct {
		Start struct {
			Line int `json:"line"`
		} `json:"start"`
	} `json:"range"`
}

func TestE2E_NavigationOverStdio(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the binary")
	}
	root, err := filepath.Abs(e2eFixture)
	if err != nil {
		t.Fatal(err)
	}
	c := startServer(t)
	c.request("initialize", map[string]any{
		"processId": nil, "rootUri": fileURI(root), "capabilities": map[string]any{},
		"workspaceFolders": []map[string]string{{"uri": fileURI(root), "name": "app"}},
	})
	c.notify("initialized", map[string]any{})

	open := func(rel string) (string, string) {
		path := filepath.Join(root, rel)
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		c.notify("textDocument/didOpen", map[string]any{"textDocument": map[string]any{
			"uri": fileURI(path), "languageId": "php", "version": 1, "text": string(src)}})
		return fileURI(path), string(src)
	}
	repoURI, repoSrc := open("app/Repositories/PostRepository.php")
	routesURI, routesSrc := open("routes/web.php")

	definition := func(uri string, pos map[string]int) []location {
		var locs []location
		raw := c.request("textDocument/definition", map[string]any{
			"textDocument": map[string]string{"uri": uri}, "position": pos})
		_ = json.Unmarshal(raw, &locs)
		return locs
	}

	// Indexing runs in the background after `initialized`; wait for it by
	// polling the simplest lookup (the string index is built in the same pass).
	readyPos := position(t, routesSrc, "config('app.name')", 9)
	deadline := time.Now().Add(e2eIndexDeadline)
	for len(definition(routesURI, readyPos)) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("index never became ready")
		}
		time.Sleep(e2ePollInterval)
	}

	tests := []struct {
		name     string
		uri, src string
		needle   string
		delta    int
		wantFile string
		wantLine int // 1-based
	}{
		{"scope via trait property and relation chain", repoURI, repoSrc, "->published()", 3, "Post.php", 13},
		{"#[Scope] attribute scope, static call", repoURI, repoSrc, "Post::popular", 7, "Post.php", 19},
		{"migration column via trait typed property", repoURI, repoSrc, "->email", 3, "create_users_table.php", 14},
		{"migration column via arrow fn param", routesURI, routesSrc, "$user->name", 8, "create_users_table.php", 13},
		{"config key inside the string", routesURI, routesSrc, "config('app.name')", 9, "app.php", 4},
		{"view name inside the string", routesURI, routesSrc, "view('home'", 6, "home.blade.php", 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			locs := definition(tc.uri, position(t, tc.src, tc.needle, tc.delta))
			if len(locs) != 1 {
				t.Fatalf("got %d locations %+v, want 1", len(locs), locs)
			}
			if !strings.HasSuffix(locs[0].URI, tc.wantFile) || locs[0].Range.Start.Line+1 != tc.wantLine {
				t.Errorf("got %s:%d, want %s:%d", locs[0].URI, locs[0].Range.Start.Line+1, tc.wantFile, tc.wantLine)
			}
		})
	}

	t.Run("no unknown-property diagnostics on valid code", func(t *testing.T) {
		// Re-send the documents now that the index is ready.
		c.notify("textDocument/didChange", map[string]any{
			"textDocument":   map[string]any{"uri": routesURI, "version": 2},
			"contentChanges": []map[string]string{{"text": routesSrc}},
		})
		c.notify("textDocument/didChange", map[string]any{
			"textDocument":   map[string]any{"uri": repoURI, "version": 2},
			"contentChanges": []map[string]string{{"text": repoSrc}},
		})
		c.request("textDocument/hover", map[string]any{ // round-trip as a barrier
			"textDocument": map[string]string{"uri": routesURI}, "position": map[string]int{"line": 0, "character": 0}})

		c.mu.Lock()
		defer c.mu.Unlock()
		for _, n := range c.notes {
			if n["method"] != "textDocument/publishDiagnostics" {
				continue
			}
			params, _ := n["params"].(map[string]any)
			if diags, _ := params["diagnostics"].([]any); len(diags) > 0 {
				t.Errorf("unexpected diagnostics for %v: %v", params["uri"], diags)
			}
		}
	})
}
