package autotest

// CDP 客户端：通过 browser-level WebSocket 驱动 Chrome。
//
// 为什么不用 rod 的高层 API：rod v0.116.2（2024 年，项目已停更）与 2026 年的
// Chrome 不兼容——Connect/Target.getTargets/Target.createTarget/Target.attachToTarget
// 单发都正常，但 PageFromTarget 内部路径会让 Chrome 直接关闭 WebSocket（EOF）。
// 协议本身完全正常（Python websockets 逐条验证过），所以这里用 coder/websocket
// 实现一个最小 JSON-RPC over CDP 客户端：只发命令、收响应，事件走独立 channel。
//
// 扁平会话模式（flatten）：所有命令带 sessionId 路由到具体标签页。

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/coder/websocket"
)

// CDP is a minimal CDP client over the browser-level WebSocket.
type CDP struct {
	conn    *websocket.Conn
	mu      sync.Mutex
	nextID  int64
	pending map[int64]chan *cdpMsg

	events chan *cdpMsg
	closed chan struct{}
}

type cdpMsg struct {
	ID        int64           `json:"id,omitempty"`
	SessionID string          `json:"sessionId,omitempty"`
	Method    string          `json:"method,omitempty"`
	Params    json.RawMessage `json:"params,omitempty"`
	Result    json.RawMessage `json:"result,omitempty"`
	Error     *cdpErr         `json:"error,omitempty"`
}

type cdpErr struct {
	Code    int64  `json:"code"`
	Message string `json:"message"`
}

func (e *cdpErr) Error() string { return fmt.Sprintf("CDP %d: %s", e.Code, e.Message) }

// DialCDP connects to the browser-level DevTools WebSocket.
func DialCDP(ctx context.Context, wsURL string) (*CDP, error) {
	conn, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{})
	if err != nil {
		return nil, fmt.Errorf("dial CDP: %w", err)
	}
	// CDP 消息可能很大（截图 base64、长 HTML），关掉默认读上限。
	conn.SetReadLimit(64 << 20)

	c := &CDP{
		conn:    conn,
		pending: map[int64]chan *cdpMsg{},
		events:  make(chan *cdpMsg, 256),
		closed:  make(chan struct{}),
	}
	go c.readLoop()
	return c, nil
}

// readLoop demultiplexes responses (routed to waiters) and events.
func (c *CDP) readLoop() {
	defer close(c.closed)
	for {
		_, data, err := c.conn.Read(context.Background())
		if err != nil {
			// 所有等待者以错误收场。
			c.mu.Lock()
			for id, ch := range c.pending {
				ch <- &cdpMsg{ID: id, Error: &cdpErr{Code: -1, Message: err.Error()}}
			}
			c.pending = map[int64]chan *cdpMsg{}
			c.mu.Unlock()
			return
		}
		var m cdpMsg
		if err := json.Unmarshal(data, &m); err != nil {
			continue // 非 JSON 帧（ping 等）忽略
		}
		if m.Method != "" {
			// 事件：满了就丢（runner 只关心少数几个事件）。
			select {
			case c.events <- &m:
			default:
			}
			continue
		}
		c.mu.Lock()
		ch, ok := c.pending[m.ID]
		if ok {
			delete(c.pending, m.ID)
		}
		c.mu.Unlock()
		if ok {
			ch <- &m
		}
	}
}

// Call sends a command and waits for its response. sessionID "" targets the
// browser itself; otherwise the command is routed to that flat session.
func (c *CDP) Call(ctx context.Context, sessionID, method string, params any) (json.RawMessage, error) {
	c.mu.Lock()
	if c.nextID == 0 {
		c.nextID = 1
	}
	id := c.nextID
	c.nextID++
	ch := make(chan *cdpMsg, 1)
	c.pending[id] = ch
	c.mu.Unlock()

	defer func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}()

	payload := map[string]any{"id": id, "method": method}
	if sessionID != "" {
		payload["sessionId"] = sessionID
	}
	if params != nil {
		payload["params"] = params
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	if err := c.conn.Write(ctx, websocket.MessageText, data); err != nil {
		return nil, fmt.Errorf("write %s: %w", method, err)
	}

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.closed:
		return nil, fmt.Errorf("CDP 连接已关闭（%s）", method)
	case m := <-ch:
		if m.Error != nil {
			return nil, fmt.Errorf("%s: %w", method, m.Error)
		}
		return m.Result, nil
	}
}

// Events exposes the event stream (load, navigation, …).
func (c *CDP) Events() <-chan *cdpMsg { return c.events }

// Close shuts the WebSocket.
func (c *CDP) Close() error {
	return c.conn.Close(websocket.StatusNormalClosure, "")
}
