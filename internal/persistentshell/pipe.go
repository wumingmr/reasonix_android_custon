package persistentshell

import (
	"os"
	"os/exec"
	"sync"

	"reasonix/internal/proc"
)

type pipeProcess struct {
	input, output *os.File
	cmd           *exec.Cmd
	job           uintptr
	once          sync.Once
}

// Both output streams share one OS pipe so command output and its trailing
// completion fence remain ordered. Track the whole process tree before the
// shell starts; cancellation must retire its children as well as the shell.
func startPipe(argv []string, dir string, env []string) (ptyConn, error) {
	if len(argv) == 0 {
		return nil, errEmptyArgv
	}
	inputReader, inputWriter, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	outputReader, outputWriter, err := os.Pipe()
	if err != nil {
		_ = inputReader.Close()
		_ = inputWriter.Close()
		return nil, err
	}
	cmd := proc.Command(argv[0], argv[1:]...)
	cmd.Dir, cmd.Env = dir, env
	cmd.Stdin, cmd.Stdout, cmd.Stderr = inputReader, outputWriter, outputWriter
	job, err := startShellTracked(cmd)
	_ = inputReader.Close()
	_ = outputWriter.Close()
	if err != nil {
		_ = inputWriter.Close()
		_ = outputReader.Close()
		return nil, err
	}
	return &pipeProcess{input: inputWriter, output: outputReader, cmd: cmd, job: job}, nil
}

func (p *pipeProcess) Read(b []byte) (int, error)  { return p.output.Read(b) }
func (p *pipeProcess) Write(b []byte) (int, error) { return p.input.Write(b) }
func (p *pipeProcess) Close() error {
	p.once.Do(func() {
		proc.KillTracked(p.cmd, p.job)
		_ = p.input.Close()
		_ = p.output.Close()
		_ = p.cmd.Wait()
	})
	return nil
}
