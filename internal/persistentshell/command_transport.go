package persistentshell

import (
	"context"
	"fmt"
	"strings"
)

type commandStage struct {
	script string
	ack    string
}

// Leave framing headroom below Darwin's 1024-byte canonical input limit.
const commandWordLimit = 512

// Keep each input below the canonical PTY limit and wait until it is consumed.
// Newlines alone do not fence Readline's terminal-mode transitions, which can
// discard queued input. Staging never runs user code; only the final eval does.
func commandStages(command, start, end string) []commandStage {
	if len(ansiCQuote(command)) <= commandWordLimit {
		return []commandStage{{script: posixCommandScript(command, start, end)}}
	}
	variable := "__rx_command_" + newMarkerID()
	var stages []commandStage
	for offset := 0; offset < len(command); offset += commandWordLimit / 4 {
		assignment := variable + "="
		if offset > 0 {
			assignment += `"$` + variable + `"`
		}
		ack := fmt.Sprintf("%s_INPUT_%d", start, offset)
		script := assignment + ansiCQuote(command[offset:min(offset+commandWordLimit/4, len(command))]) +
			"; printf '%s\\n' " + posixQuote(ack) + "\n"
		stages = append(stages, commandStage{script: script, ack: ack})
	}
	return append(stages, commandStage{script: commandWordScript(`"$`+variable+`"`, start, end, "; unset "+variable)})
}

func (s *session) writeCommand(ctx context.Context, command, start, end string) error {
	for _, stage := range commandStages(command, start, end) {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := s.writeScript(stage.script); err != nil {
			return err
		}
		if stage.ack == "" {
			return nil
		}
		var pending string
		if err := s.pump(ctx, func(text string) bool {
			pending += text
			if strings.Contains(pending, stage.ack+"\n") {
				return true
			}
			if len(pending) > markerOverlap {
				pending = pending[len(pending)-markerOverlap:]
			}
			return false
		}); err != nil {
			return err
		}
	}
	return nil
}
