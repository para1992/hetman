package engine

import (
	"os"
	"os/exec"
	"strconv"
)

func configure(*exec.Cmd) {}

// killTree kills the process and all of its descendants with the system
// taskkill tool, falling back to killing the process alone.
func killTree(p *os.Process) {
	kill := exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(p.Pid))
	if err := kill.Run(); err != nil {
		p.Kill()
	}
}
