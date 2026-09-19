package main

/*
#include <unistd.h>
*/
import "C"

import (
	"os"
	"unsafe"

	androidcli "github.com/sagernet/sing-box/cmd/sing-box"
)

// AndroidBoxCLI_v1 is only for a standalone process, never a JNI caller.
//
//export AndroidBoxCLI_v1
func AndroidBoxCLI_v1(argc C.int, argv **C.char, stdoutFD, stderrFD C.int) C.int {
	if C.dup2(stdoutFD, 1) < 0 || C.dup2(stderrFD, 2) < 0 {
		return 70
	}
	C.close(stdoutFD)
	C.close(stderrFD)
	args := unsafe.Slice(argv, int(argc))
	os.Args = make([]string, len(args))
	for i, arg := range args {
		os.Args[i] = C.GoString(arg)
	}
	androidcli.Run()
	return 0
}
