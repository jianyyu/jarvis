package protocol

import (
	"net"
	"testing"
)

func TestCodecRoundTrip(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	serverCodec := NewCodec(server)
	clientCodec := NewCodec(client)

	// Send request from client
	go func() {
		clientCodec.Send(Request{Action: "ping"})
	}()

	var req Request
	if err := serverCodec.Receive(&req); err != nil {
		t.Fatalf("receive: %v", err)
	}
	if req.Action != "ping" {
		t.Errorf("action: got %q, want %q", req.Action, "ping")
	}

	// Send response from server
	go func() {
		serverCodec.Send(Response{Event: "pong"})
	}()

	var resp Response
	if err := clientCodec.Receive(&resp); err != nil {
		t.Fatalf("receive: %v", err)
	}
	if resp.Event != "pong" {
		t.Errorf("event: got %q, want %q", resp.Event, "pong")
	}
}

func TestCodecSendInput(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	clientCodec := NewCodec(client)
	serverCodec := NewCodec(server)

	go func() {
		clientCodec.Send(Request{Action: "send_input", Text: "hello\n"})
	}()

	var req Request
	serverCodec.Receive(&req)
	if req.Text != "hello\n" {
		t.Errorf("text: got %q, want %q", req.Text, "hello\n")
	}
}
