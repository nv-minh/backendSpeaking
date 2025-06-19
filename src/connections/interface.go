// src/connections/interface.go
package connections

import (
	"net/http"
	"time"
)

// Upgrader là interface chung cho việc nâng cấp kết nối HTTP -> WebSocket.
type Upgrader interface {
	Upgrade(w http.ResponseWriter, r *http.Request) (Conn, error)
}

// Conn là interface chung cho một kết nối WebSocket.
type Conn interface {
	ReadMessage() (messageType int, p []byte, err error)
	WriteMessage(messageType int, data []byte) error
	Close() error
	IsClosed() bool
	GetLastActiveTime() time.Time
	IsStale(timeout time.Duration) bool
	GetID() string
	GetType() string
}
