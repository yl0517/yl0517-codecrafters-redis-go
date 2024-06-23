package protocol

import (
	"bufio"
	"fmt"
	"net"
)

// Connection represents a connection between a client and a server.
type Connection struct {
	conn   net.Conn
	reader *bufio.Reader
}

// NewConnection creates a new Connection instance.
func NewConnection(c net.Conn) *Connection {
	return &Connection{
		conn:   c,
		reader: bufio.NewReader(c),
	}
}

// Close closes the connection.
func (c *Connection) Close() error {
	return c.conn.Close()
}

// GetLine returns an individual line from a command without CRLF
func (c *Connection) GetLine() (string, error) {
	s, err := c.reader.ReadString('\n')

	if len(s) > 0 {
		if s[len(s)-1] == '\n' {
			s = s[:len(s)-1]
		}

		if s[len(s)-1] == '\r' {
			s = s[:len(s)-1]
		}
	}

	return s, err
}

// Write writes the given string to the connection
func (c *Connection) Write(s string) error {
	var written int

	for written < len(s) {
		n, err := c.conn.Write([]byte(s[written:]))
		if err != nil {
			return fmt.Errorf("Write failed: %v", err)
		}
		written += n
	}
	return nil
}
