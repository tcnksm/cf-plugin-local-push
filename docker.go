package main

import (
	"io"
	"io/ioutil"
	"os/exec"
)

type Docker struct {
	OutStream io.Writer
	InStream  io.Reader
	Discard   bool
}

// docker runs docker command
func (d *Docker) execute(args ...string) error {
	cmd := exec.Command("docker", args...)

	cmd.Stdin = d.InStream
	cmd.Stderr = d.OutStream
	cmd.Stdout = d.OutStream

	if d.Discard {
		cmd.Stderr = ioutil.Discard
		cmd.Stdout = ioutil.Discard
	}

	if err := cmd.Run(); err != nil {
		return err
	}

	return nil
}
