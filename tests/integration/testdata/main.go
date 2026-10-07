package main

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"syscall"
	"time"
)

func main() {
	if len(os.Args) != 2 {
		os.Exit(2)
	}
	switch os.Args[1] {
	case "security":
		securityProbe(true)
	case "security-unconfined":
		securityProbe(false)
	case "network-server":
		networkServer(true)
	case "network-service":
		networkServer(false)
	case "network-client":
		networkClient()
	case "cpu":
		runtime.GOMAXPROCS(1)
		var before, after syscall.Rusage
		if err := syscall.Getrusage(syscall.RUSAGE_SELF, &before); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Println("cpu workload ready")
		start := time.Now()
		deadline := start.Add(4 * time.Second)
		var checksum uint64 = 1
		for time.Now().Before(deadline) {
			for i := 0; i < 65536; i++ {
				checksum = checksum*1664525 + 1013904223
			}
		}
		elapsed := time.Since(start)
		if err := syscall.Getrusage(syscall.RUSAGE_SELF, &after); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		cpuNS := after.Utime.Nano() + after.Stime.Nano() - before.Utime.Nano() - before.Stime.Nano()
		fmt.Printf("elapsed_us=%d cpu_us=%d checksum=%d\n", elapsed.Microseconds(), cpuNS/1000, checksum)
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
