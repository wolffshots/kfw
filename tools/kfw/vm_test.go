package main

import "testing"

// An unknown subcommand or a stray argument must fail before any device boots.
func TestVMRejectsUnknownArgs(t *testing.T) {
	for _, args := range [][]string{
		{"list"},
		{"-time", "1s", "list"},
		{"start", "extra"},
	} {
		code, err := cmdVM(args)
		if code != 2 || err == nil {
			t.Errorf("cmdVM(%q) = %d, %v; want 2 and an error", args, code, err)
		}
	}
}
