package protocol

import (
	"encoding/json"
	"net"
)

// Codec reads and writes newline-delimited JSON over a net.Conn.
type Codec struct {
	conn    net.Conn
	encoder *json.Encoder
	decoder *json.Decoder
}

func NewCodec(conn net.Conn) *Codec {
	return &Codec{
		conn:    conn,
		encoder: json.NewEncoder(conn),
		decoder: json.NewDecoder(conn),
	}
}

func (c *Codec) Send(msg any) error {
	return c.encoder.Encode(msg)
}

func (c *Codec) Receive(msg any) error {
	return c.decoder.Decode(msg)
}

func (c *Codec) Close() error {
	return c.conn.Close()
}
