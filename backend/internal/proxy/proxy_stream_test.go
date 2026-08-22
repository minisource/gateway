package proxy

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/valyala/fasthttp"
)

// startTestGateway binds a real TCP listener so the hijack-based stream path
// can be exercised end-to-end (fiber's in-memory Test transport cannot
// hijack a real connection).
func startTestGateway(t *testing.T, p *ServiceProxy) string {
	t.Helper()
	app := fiber.New()
	app.Get("/sse", func(c *fiber.Ctx) error { return p.Forward(c, "test", "") })
	app.Get("/ws", func(c *fiber.Ctx) error { return p.Forward(c, "test", "") })
	app.Get("/json", func(c *fiber.Ctx) error { return p.Forward(c, "test", "") })

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() { _ = app.Listener(ln) }()
	return ln.Addr().String()
}

// TestStreamSSEProxiedWithoutBuffering verifies an SSE stream reaches the
// client chunk-by-chunk: the upstream sends two chunks with a 500ms gap, so a
// buffering proxy would withhold the first chunk until the stream closes.
func TestStreamSSEProxiedWithoutBuffering(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
			t.Errorf("upstream accept header = %q", r.Header.Get("Accept"))
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)
		fl := w.(http.Flusher)
		_, _ = fmt.Fprint(w, "data: first\n\n")
		fl.Flush()
		time.Sleep(500 * time.Millisecond)
		_, _ = fmt.Fprint(w, "data: second\n\n")
		fl.Flush()
	}))
	t.Cleanup(upstream.Close)

	p := &ServiceProxy{services: map[string]*ServiceClient{
		"test": {Name: "test", URL: upstream.URL, Healthy: true, Client: &fasthttp.Client{ReadTimeout: 15 * time.Second}},
	}}
	gwAddr := startTestGateway(t, p)

	req, _ := http.NewRequest(http.MethodGet, "http://"+gwAddr+"/sse", nil)
	req.Header.Set("Accept", "text/event-stream")

	start := time.Now()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("gateway SSE request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("content-type = %q, want text/event-stream", ct)
	}

	br := bufio.NewReader(resp.Body)
	firstLine, err := br.ReadString('\n')
	if err != nil {
		t.Fatalf("read first chunk: %v", err)
	}
	firstElapsed := time.Since(start)
	if !strings.Contains(firstLine, "data: first") {
		t.Fatalf("first line = %q, want 'data: first'", firstLine)
	}
	// The first chunk must arrive before the upstream's 500ms sleep ends —
	// otherwise the proxy buffered the whole response.
	if firstElapsed >= 400*time.Millisecond {
		t.Fatalf("first chunk arrived after %v — proxy appears to buffer SSE", firstElapsed)
	}

	// Frames are "data: ...\n\n", so consume blank separators until the second
	// data line arrives (proving the stream stayed open past the first chunk).
	second := ""
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("read second chunk: %v", err)
		}
		if strings.Contains(line, "data: second") {
			second = line
			break
		}
	}
	if second == "" {
		t.Fatal("second data line not received")
	}
}

// TestStreamWebSocketUpgradeAndEcho verifies the 101 handshake headers are
// relayed untouched and bytes flow bidirectionally through the tunnel.
func TestStreamWebSocketUpgradeAndEcho(t *testing.T) {
	upstream, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("upstream listen: %v", err)
	}
	defer upstream.Close()

	got := make(chan string, 1)
	go func() {
		conn, err := upstream.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		br := bufio.NewReader(conn)
		reqLine, _ := br.ReadString('\n')
		var headers strings.Builder
		for {
			line, err := br.ReadString('\n')
			if err != nil {
				return
			}
			if line == "\r\n" || line == "\n" {
				break
			}
			headers.WriteString(line)
		}
		got <- reqLine + headers.String()
		_, _ = fmt.Fprint(conn, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n")
		_, _ = conn.Write([]byte("HELLO"))
		// Echo the next client frame.
		buf := make([]byte, 64)
		n, err := conn.Read(buf)
		if err == nil {
			_, _ = conn.Write(buf[:n])
		}
	}()

	p := &ServiceProxy{services: map[string]*ServiceClient{
		"test": {Name: "test", URL: "http://" + upstream.Addr().String(), Healthy: true, Client: &fasthttp.Client{ReadTimeout: 15 * time.Second}},
	}}
	gwAddr := startTestGateway(t, p)

	conn, err := net.DialTimeout("tcp", gwAddr, 5*time.Second)
	if err != nil {
		t.Fatalf("dial gateway: %v", err)
	}
	defer conn.Close()

	handshake := "GET /ws HTTP/1.1\r\nHost: " + gwAddr + "\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n" +
		"Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Version: 13\r\n\r\n"
	if _, err := conn.Write([]byte(handshake)); err != nil {
		t.Fatalf("write handshake: %v", err)
	}

	br := bufio.NewReader(conn)
	statusLine, err := br.ReadString('\n')
	if err != nil {
		t.Fatalf("read status: %v", err)
	}
	if !strings.Contains(statusLine, "101") {
		t.Fatalf("status line = %q, want 101 Switching Protocols", statusLine)
	}
	var respHeaders strings.Builder
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("read header: %v", err)
		}
		if line == "\r\n" {
			break
		}
		respHeaders.WriteString(line)
	}
	if !strings.Contains(respHeaders.String(), "Upgrade: websocket") {
		t.Fatalf("missing Upgrade header in relayed response:\n%s", respHeaders.String())
	}

	hello := make([]byte, 5)
	if _, err := io.ReadFull(br, hello); err != nil {
		t.Fatalf("read upstream payload: %v", err)
	}
	if string(hello) != "HELLO" {
		t.Fatalf("payload = %q, want HELLO", hello)
	}

	// Bidirectional relay: send a frame, expect the upstream echo.
	if _, err := conn.Write([]byte("PING")); err != nil {
		t.Fatalf("write frame: %v", err)
	}
	echo := make([]byte, 4)
	if _, err := io.ReadFull(br, echo); err != nil {
		t.Fatalf("read echo: %v", err)
	}
	if string(echo) != "PING" {
		t.Fatalf("echo = %q, want PING", echo)
	}

	// Upstream must have received the real upgrade request (headers relayed).
	// Header names are normalized (e.g. Sec-Websocket-Key) so assert on the
	// value, which is case-stable.
	select {
	case upstreamReq := <-got:
		if !strings.Contains(upstreamReq, "dGhlIHNhbXBsZSBub25jZQ==") {
			t.Fatalf("upstream request missing websocket key:\n%q", upstreamReq)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for upstream to receive the request")
	}
}

// TestBufferedPathStillWorks guards the non-streaming path against regressions
// from the stream branch: a plain JSON response must still round-trip.
func TestBufferedPathStillWorks(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"ok":true}`)
	}))
	t.Cleanup(upstream.Close)

	p := &ServiceProxy{services: map[string]*ServiceClient{
		"test": {Name: "test", URL: upstream.URL, Healthy: true, Client: &fasthttp.Client{ReadTimeout: 15 * time.Second}},
	}}
	gwAddr := startTestGateway(t, p)

	resp, err := http.Get("http://" + gwAddr + "/json")
	if err != nil {
		t.Fatalf("gateway request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != `{"ok":true}` {
		t.Fatalf("body = %q, want {\"ok\":true}", body)
	}
}

func TestIsStreamingRequest(t *testing.T) {
	app := fiber.New()
	cases := []struct {
		name       string
		upgrade    string
		connection string
		accept     string
		want       bool
	}{
		{name: "websocket upgrade", upgrade: "websocket", connection: "Upgrade", want: true},
		{name: "websocket upgrade lowercase", upgrade: "WebSocket", connection: "keep-alive, Upgrade", want: true},
		{name: "sse accept", accept: "text/event-stream", want: true},
		{name: "sse accept with other types", accept: "text/html, text/event-stream", want: true},
		{name: "upgrade header without websocket", upgrade: "h2c", connection: "Upgrade", want: false},
		{name: "plain json", accept: "application/json", want: false},
		{name: "no streaming headers", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := &fasthttp.RequestCtx{}
			if tc.upgrade != "" {
				raw.Request.Header.Set("Upgrade", tc.upgrade)
			}
			if tc.connection != "" {
				raw.Request.Header.Set("Connection", tc.connection)
			}
			if tc.accept != "" {
				raw.Request.Header.Set("Accept", tc.accept)
			}
			ctx := app.AcquireCtx(raw)
			defer app.ReleaseCtx(ctx)
			if got := isStreamingRequest(ctx); got != tc.want {
				t.Fatalf("isStreamingRequest(%s/%s/%s) = %v, want %v", tc.upgrade, tc.connection, tc.accept, got, tc.want)
			}
		})
	}
}

func TestUpstreamHostPort(t *testing.T) {
	cases := []struct {
		baseURL string
		want    string
		wantErr bool
	}{
		{baseURL: "http://127.0.0.1:9001", want: "127.0.0.1:9001"},
		{baseURL: "http://auth-backend:9001", want: "auth-backend:9001"},
		{baseURL: "http://auth-backend", want: "auth-backend:80"},
		{baseURL: "https://auth-backend", want: "auth-backend:443"},
		{baseURL: "http://[::1", wantErr: true},
	}
	for _, tc := range cases {
		got, err := upstreamHostPort(tc.baseURL)
		if tc.wantErr {
			if err == nil {
				t.Fatalf("upstreamHostPort(%q) expected error, got %q", tc.baseURL, got)
			}
			continue
		}
		if err != nil {
			t.Fatalf("upstreamHostPort(%q): %v", tc.baseURL, err)
		}
		if got != tc.want {
			t.Fatalf("upstreamHostPort(%q) = %q, want %q", tc.baseURL, got, tc.want)
		}
	}
}
