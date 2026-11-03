package main

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/lucasmendoncca/OrbMQ/internal/auth"
)

func main() {
	if len(os.Args) != 1 {
		fmt.Fprintln(os.Stderr, "password must be supplied through stdin")
		os.Exit(1)
	}
	if err := run(os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(input io.Reader, output io.Writer) error {
	password, err := io.ReadAll(io.LimitReader(input, 65536))
	if err != nil {
		return errors.New("read password failed")
	}
	if len(password) > 65535 {
		return errors.New("password exceeds mqtt binary field limit")
	}
	hash, err := auth.HashPassword(string(password))
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(output, hash)
	return err
}
