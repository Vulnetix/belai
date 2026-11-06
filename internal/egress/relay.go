package egress

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"syscall"
)

// Command is the hidden subcommand that runs a sandboxed command with a relay
// beside it: `belai __egress-exec -sock S -port N -- argv`. sandbox.HelperMain
// dispatches it, the way it dispatches the Landlock helper.
const Command = "__egress-exec"

// Relay accepts connections on ln (the loopback port inside the sandbox) and
// copies each to the proxy's unix socket. It adds no policy of its own: the
// proxy decides. It returns when ln is closed.
func Relay(ln net.Listener, sock string) {
	var wg sync.WaitGroup
	defer wg.Wait()
	for {
		c, err := ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer c.Close()
			up, err := net.Dial("unix", sock)
			if err != nil {
				return
			}
			defer up.Close()
			var cp sync.WaitGroup
			cp.Add(2)
			go func() {
				defer cp.Done()
				_, _ = io.Copy(up, c)
				closeWrite(up)
			}()
			go func() {
				defer cp.Done()
				_, _ = io.Copy(c, up)
				closeWrite(c)
			}()
			cp.Wait()
		}()
	}
}

func closeWrite(c net.Conn) {
	if cw, ok := c.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
	}
}

// ExecMain is the helper: it listens on 127.0.0.1:port, relays to the proxy at
// sock, runs argv as a child with this process's environment and standard
// streams, and exits with the child's status. Exit 125: the relay could not be
// set up and nothing ran. Exit 127: the command could not be started.
func ExecMain(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet(Command, flag.ContinueOnError)
	fs.SetOutput(stderr)
	sock := fs.String("sock", "", "proxy unix socket")
	port := fs.Int("port", 0, "loopback port for the relay")
	if err := fs.Parse(args); err != nil {
		return 125
	}
	argv := fs.Args()
	if *sock == "" || !filepath.IsAbs(*sock) || *port < 1 || *port > 65535 || len(argv) == 0 {
		fmt.Fprintf(stderr, "usage: belai %s -sock PATH -port N -- command [args]\n", Command)
		return 125
	}
	// The relay must go with its parent, and the child with the relay.
	runtime.LockOSThread()
	if err := dieWithParent(); err != nil {
		fmt.Fprintf(stderr, "belai %s: %v\n", Command, err)
		return 125
	}
	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(*port)))
	if err != nil {
		fmt.Fprintf(stderr, "belai %s: cannot listen for the relay: %v; nothing was run\n", Command, err)
		return 125
	}
	defer ln.Close()
	done := make(chan struct{})
	go func() { defer close(done); Relay(ln, *sock) }()

	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.SysProcAttr = childAttr()
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(stderr, "belai %s: %v\n", Command, err)
		return 127
	}
	sigs := make(chan os.Signal, 4)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	go func() {
		for s := range sigs {
			_ = cmd.Process.Signal(s)
		}
	}()
	err = cmd.Wait()
	signal.Stop(sigs)
	close(sigs)
	_ = ln.Close()
	<-done
	return exitStatus(err)
}

// exitStatus maps a child's wait result to this process's exit code, the way a
// shell does: 128 plus the signal for a signalled child.
func exitStatus(err error) int {
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			return 128 + int(ws.Signal())
		}
		if c := ee.ExitCode(); c >= 0 {
			return c
		}
	}
	return 1
}
