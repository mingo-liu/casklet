package main

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"time"
)

func main() {
	if len(os.Args) != 2 {
		os.Exit(2)
	}
	switch os.Args[1] {
	case "memory":
		fmt.Println("allocating")
		blocks := make([][]byte, 0, 64)
		for i := 0; i < 64; i++ {
			block := make([]byte, 4<<20)
			for j := 0; j < len(block); j += 4096 {
				block[j] = 1
			}
			blocks = append(blocks, block)
			time.Sleep(10 * time.Millisecond)
		}
		fmt.Println("unexpected allocation success")
		runtime.KeepAlive(blocks)
		os.Exit(1)
	case "pids":
		var children []*exec.Cmd
		defer func() {
			for _, child := range children {
				_ = child.Process.Kill()
				_ = child.Wait()
			}
		}()
		for i := 0; i < 128; i++ {
			child := exec.Command("/bin/sleep", "20")
			if err := child.Start(); err != nil {
				fmt.Printf("process creation denied after %d children: %v\n", i, err)
				return
			}
			children = append(children, child)
		}
		fmt.Println("unexpected process creation success")
		os.Exit(1)
	default:
		os.Exit(2)
	}
}
