package persistentshell

import (
	"bytes"
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
	"unicode/utf16"

	"reasonix/internal/proc"
	"reasonix/internal/tool"
)

//go:embed powershell.ps1
var powershellBootstrap string

const maxControlFrame = 4 << 20

type shellFrame struct {
	Version  int    `json:"version"`
	Kind     string `json:"kind"`
	ID       string `json:"id"`
	Command  string `json:"command,omitempty"`
	Start    string `json:"start,omitempty"`
	End      string `json:"end,omitempty"`
	ExitCode *int   `json:"exitCode,omitempty"`
}

func readShellFrame(r io.Reader) (shellFrame, error) {
	var size uint32
	if err := binary.Read(r, binary.LittleEndian, &size); err != nil {
		return shellFrame{}, err
	}
	if size == 0 || size > maxControlFrame {
		return shellFrame{}, errors.New("invalid shell control frame size")
	}
	data := make([]byte, size)
	if _, err := io.ReadFull(r, data); err != nil {
		return shellFrame{}, err
	}
	var frame shellFrame
	if err := json.Unmarshal(data, &frame); err != nil {
		return frame, err
	}
	if frame.Version != 1 {
		return frame, errors.New("unsupported shell control protocol")
	}
	return frame, nil
}

func writeShellFrame(w io.Writer, frame shellFrame) error {
	data, err := json.Marshal(frame)
	if err != nil {
		return err
	}
	if len(data) > maxControlFrame {
		return errors.New("shell command exceeds control frame limit")
	}
	if err := binary.Write(w, binary.LittleEndian, uint32(len(data))); err != nil {
		return err
	}
	_, err = io.Copy(w, bytes.NewReader(data))
	return err
}

// encodedPowerShell uses the encoding required by both Windows PowerShell and pwsh.
func encodedPowerShell(script string) string {
	units := utf16.Encode([]rune(script))
	data := make([]byte, len(units)*2)
	for i, unit := range units {
		binary.LittleEndian.PutUint16(data[i*2:], unit)
	}
	return base64.StdEncoding.EncodeToString(data)
}

type powershellProcess struct {
	control net.Conn
	output  *os.File
	cmd     *exec.Cmd
	job     uintptr
	once    sync.Once
	stop    chan struct{}
}

func (p *powershellProcess) Read(b []byte) (int, error) { return p.output.Read(b) }
func (p *powershellProcess) Write([]byte) (int, error) {
	return 0, errors.New("use shell control channel")
}
func (p *powershellProcess) Close() error {
	p.once.Do(func() {
		close(p.stop)
		if p.control != nil {
			_ = p.control.Close()
		}
		proc.KillTracked(p.cmd, p.job)
		_ = p.output.Close()
		_ = p.cmd.Wait()
	})
	return nil
}

func startPowerShell(req Request, fp string) (*session, error) {
	name := "rx-" + rand.Text()
	listener, err := listenShellControl(name)
	if err != nil {
		return nil, err
	}
	defer listener.Close()
	reader, writer, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	cmd := proc.Command(req.Argv[0], req.Argv[1:]...)
	cmd.Dir, cmd.Env = req.Dir, append(append([]string(nil), req.Env...), "REASONIX_PWSH_PIPE="+name)
	cmd.Stdout, cmd.Stderr = writer, writer
	job, err := startShellTracked(cmd)
	_ = writer.Close()
	if err != nil {
		_ = reader.Close()
		return nil, err
	}
	p := &powershellProcess{output: reader, cmd: cmd, job: job, stop: make(chan struct{})}
	// Windows PowerShell 5.1 can take more than 10 seconds to initialize on a
	// cold or contended host. Bound the complete connect-and-ready handshake by
	// one deadline so a late connection cannot silently start a second budget.
	deadline := time.Now().Add(powerShellStartupTimeout)
	type accepted struct {
		conn net.Conn
		err  error
	}
	ready := make(chan accepted, 1)
	go func() { conn, err := listener.Accept(); ready <- accepted{conn, err} }()
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	select {
	case result := <-ready:
		if result.err != nil {
			_ = p.Close()
			return nil, result.err
		}
		p.control = result.conn
	case <-ctx.Done():
		_ = listener.Close()
		result := <-ready
		if result.conn != nil {
			_ = result.conn.Close()
		}
		_ = p.Close()
		return nil, ctx.Err()
	}
	_ = p.control.SetDeadline(deadline)
	frame, err := readShellFrame(p.control)
	if err != nil || frame.Kind != "ready" {
		_ = p.Close()
		if err == nil {
			err = errors.New("missing ready frame")
		}
		return nil, fmt.Errorf("PowerShell handshake failed: %w", err)
	}
	_ = p.control.SetDeadline(time.Time{})
	s := &session{conn: p, fp: fp, powershell: p}
	s.startReader()
	return s, nil
}

func (s *session) runPowerShell(ctx context.Context, req Request) Result {
	runCtx := ctx
	if req.Timeout > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, req.Timeout)
		defer cancel()
	}
	id := newMarkerID() + newMarkerID()
	start, end := "RX_PWSH_START_"+id, "RX_PWSH_END_"+id+":"
	capt := newCapture(start, end, req.Progress)
	result := Result{Started: true}
	type response struct {
		frame shellFrame
		err   error
	}
	control := make(chan response, 1)
	go func() {
		err := writeShellFrame(s.powershell.control, shellFrame{Version: 1, Kind: "run", ID: id, Command: req.Command, Start: start, End: end})
		if err != nil {
			control <- response{err: err}
			return
		}
		for _, kind := range []string{"started", "completed"} {
			frame, err := readShellFrame(s.powershell.control)
			if err == nil && (frame.Kind != kind || frame.ID != id) {
				err = errors.New("unexpected shell command receipt")
			}
			if err != nil || kind == "completed" {
				control <- response{frame, err}
				return
			}
		}
	}()
	var completion *int
	var failure error
	for failure == nil && !(capt.done && completion != nil) {
		select {
		case <-runCtx.Done():
			failure = runCtx.Err()
		case receipt := <-control:
			failure = receipt.err
			completion = receipt.frame.ExitCode
			if failure == nil && completion == nil {
				failure = errors.New("missing shell exit status")
			}
		case chunk := <-s.pendingRead:
			capt.push(string(chunk.data))
			if chunk.err != nil && !capt.done {
				failure = chunk.err
			}
		}
	}
	if failure == nil && *completion != capt.exitCode {
		failure = errors.New("shell output fence disagrees with completion")
	}
	if failure == nil {
		result.Output, result.ExitCode, result.ExitCodeKnown = capt.body(), *completion, true
		result.State = tool.ShellStateCompleted
		if *completion != 0 {
			result.State, result.FailurePhase, result.Err = tool.ShellStateFailed, tool.ShellPhaseExecution, fmt.Errorf("exit status %d", *completion)
		}
		return result
	}
	// A broken pipe may stop halfway through the private output fence.
	// Do not release that protocol suffix to progress or model output.
	if at := strings.Index(capt.hold, end); at >= 0 {
		capt.hold = capt.hold[:at]
	} else {
		for size := min(len(capt.hold), len(end)-1); size > 0; size-- {
			if strings.HasSuffix(capt.hold, end[:size]) {
				capt.hold = capt.hold[:len(capt.hold)-size]
				break
			}
		}
	}
	result.Output, result.Err, result.ShellDied = capt.partial(), failure, true
	result.State, result.FailurePhase = tool.ShellStateFailed, tool.ShellPhaseExecution
	if errors.Is(failure, context.Canceled) {
		result.Canceled = true
		result.State, result.FailurePhase = tool.ShellStateCancelled, tool.ShellPhaseCancellation
	}
	if errors.Is(failure, context.DeadlineExceeded) {
		result.TimedOut = true
		result.State, result.FailurePhase = tool.ShellStateTimedOut, tool.ShellPhaseTimeout
	}
	s.markClosed()
	return result
}
