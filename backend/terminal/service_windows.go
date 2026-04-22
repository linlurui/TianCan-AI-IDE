//go:build windows

package terminal

import (
	"fmt"
	"sync"
)

type Service struct {
	mu      sync.Mutex
	port    int
	running bool
}

func NewService() *Service { return &Service{} }

func (s *Service) StartAndGetPort() (int, error) {
	return 0, fmt.Errorf("terminal not yet supported on Windows")
}
