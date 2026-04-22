//go:build !windows

package terminal

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"sync"

	"github.com/coder/websocket"
	"github.com/creack/pty"
)

// Service manages the terminal PTY WebSocket server.
type Service struct {
	mu      sync.Mutex
	port    int
	running bool
}

// NewService creates a new terminal service.
func NewService() *Service {
	return &Service{}
}

// StartAndGetPort starts the WebSocket PTY server (idempotent) and returns the port.
func (s *Service) StartAndGetPort() (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running {
		return s.port, nil
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, fmt.Errorf("cannot bind terminal server: %w", err)
	}
	s.port = l.Addr().(*net.TCPAddr).Port

	mux := http.NewServeMux()
	mux.HandleFunc("/pty", handlePTY)

	go func() {
		if err := http.Serve(l, mux); err != nil {
			log.Printf("terminal server stopped: %v", err)
		}
	}()

	s.running = true
	return s.port, nil
}

type resizeMsg struct {
	Type string `json:"type"`
	Cols uint16 `json:"cols"`
	Rows uint16 `json:"rows"`
}

type setlangMsg struct {
	Type string `json:"type"`
	Lang string `json:"lang"`
}

// localeFromLang returns a LANG= environment variable string for the given language code.
func localeFromLang(lang string) string {
	switch lang {
	case "en", "en-US", "en-GB":
		return "LANG=en_US.UTF-8"
	case "zh", "zh-CN", "zh-TW":
		return "LANG=zh_CN.UTF-8"
	case "ja":
		return "LANG=ja_JP.UTF-8"
	case "ko":
		return "LANG=ko_KR.UTF-8"
	default:
		return "LANG=en_US.UTF-8"
	}
}

func handlePTY(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true,
	})
	if err != nil {
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	defer conn.Close(websocket.StatusNormalClosure, "")

	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/bash"
	}

	// Determine locale from query param (?lang=en or ?lang=zh)
	lang := r.URL.Query().Get("lang")
	localeEnv := localeFromLang(lang)

	cmd := exec.Command(shell)
	cmd.Env = append(os.Environ(),
		"TERM=xterm-256color",
		"COLORTERM=truecolor",
		localeEnv,
	)

	ptmx, err := pty.Start(cmd)
	if err != nil {
		conn.Write(ctx, websocket.MessageText, []byte("\r\nFailed to start shell: "+err.Error()+"\r\n"))
		return
	}
	defer func() {
		ptmx.Close()
		cmd.Process.Kill()
		cmd.Wait()
	}()

	// PTY output → WebSocket
	go func() {
		buf := make([]byte, 4096)
		for {
			n, readErr := ptmx.Read(buf)
			if n > 0 {
				if werr := conn.Write(ctx, websocket.MessageBinary, buf[:n]); werr != nil {
					cancel()
					return
				}
			}
			if readErr != nil {
				cancel()
				return
			}
		}
	}()

	// WebSocket input → PTY / resize
	for {
		msgType, data, err := conn.Read(ctx)
		if err != nil {
			return
		}
		if msgType == websocket.MessageText && len(data) > 2 && data[0] == '{' {
			var rm resizeMsg
			if json.Unmarshal(data, &rm) == nil && rm.Type == "resize" {
				pty.Setsize(ptmx, &pty.Winsize{Cols: rm.Cols, Rows: rm.Rows})
			}
			// setlang is informational — locale is set at shell startup via query param
		} else {
			ptmx.Write(data)
		}
	}
}
