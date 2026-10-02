package skillwatch

import (
	"encoding/json"
	"fmt"
	"io"
)

// wireKind identifies one frame of the host <-> watcher helper pipe protocol.
// The helper process is an internal entry of the existing host executable; the
// protocol must stay compatible across a helper and host built from the same
// source tree, which the host enforces by spawning its own executable.
type wireKind uint8

const (
	// Host -> helper.
	wireRegister wireKind = 1 // watch dirs under one logical registration
	wireCancel   wireKind = 2 // revoke a logical registration
	wireShutdown wireKind = 3 // helper exits after replying
	wirePing     wireKind = 4 // control-path liveness probe

	// Helper -> host.
	wireReady      wireKind = 5 // helper finished initializing
	wireRegistered wireKind = 6 // registration confirmed
	wireEvent      wireKind = 7 // coalescing input: one filesystem event
	wireError      wireKind = 8 // registration or backend failure
	wirePong       wireKind = 9 // ping reply
)

// Op flags mirror the fsnotify operations the service reacts to. Keeping the
// protocol independent of fsnotify types lets the helper binary compile without
// dragging host-side assumptions across the pipe.
type Op uint32

const (
	OpCreate Op = 1 << iota
	OpRemove
	OpRename
	OpWrite
	OpChmod
)

func (o Op) String() string {
	switch o {
	case OpCreate:
		return "create"
	case OpRemove:
		return "remove"
	case OpRename:
		return "rename"
	case OpWrite:
		return "write"
	case OpChmod:
		return "chmod"
	}
	return "op"
}

const wireMaxFrameBytes = 8 << 20

// frame is one length-prefixed JSON message. ID is a logical registration;
// RootGen is the host generation the registration was created under, so late
// events from a superseded registration stay recognizable and get dropped.
type frame struct {
	Kind    wireKind `json:"kind"`
	ID      uint64   `json:"id,omitempty"`
	RootGen uint64   `json:"rootGen,omitempty"`
	Op      Op       `json:"op,omitempty"`
	Dirs    []string `json:"dirs,omitempty"`
	Msg     string   `json:"msg,omitempty"`
}

func writeFrame(w io.Writer, f frame) error {
	payload, err := json.Marshal(f)
	if err != nil {
		return err
	}
	if len(payload) > wireMaxFrameBytes {
		return fmt.Errorf("watch helper frame exceeds %d bytes", wireMaxFrameBytes)
	}
	var size [4]byte
	n := uint32(len(payload))
	size[0], size[1], size[2], size[3] = byte(n), byte(n>>8), byte(n>>16), byte(n>>24)
	if _, err := w.Write(size[:]); err != nil {
		return err
	}
	_, err = w.Write(payload)
	return err
}

func readFrame(r io.Reader) (frame, error) {
	var size [4]byte
	if _, err := io.ReadFull(r, size[:]); err != nil {
		return frame{}, err
	}
	n := uint32(size[0]) | uint32(size[1])<<8 | uint32(size[2])<<16 | uint32(size[3])<<24
	if n == 0 || n > wireMaxFrameBytes {
		return frame{}, fmt.Errorf("watch helper frame size %d out of range", n)
	}
	payload := make([]byte, n)
	if _, err := io.ReadFull(r, payload); err != nil {
		return frame{}, err
	}
	var f frame
	if err := json.Unmarshal(payload, &f); err != nil {
		return frame{}, err
	}
	return f, nil
}
