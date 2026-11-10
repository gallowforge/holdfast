//go:build !unix && !js

package world

import "os/exec"

func ownGroup(*exec.Cmd) {}
