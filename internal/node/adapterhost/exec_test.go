package adapterhost_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/aianyai/sidera/internal/node/adapterhost"
	"github.com/aianyai/sidera/internal/node/config"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
	"github.com/aianyai/sidera/sdk/go/manifest"
	"github.com/aianyai/sidera/sdk/go/peer"
)

// The test binary is its own adapter: started with ADAPTERHOST_HELPER set it acts
// as the program the launcher is told to start, and never runs a test.
const (
	helperEnv  = "ADAPTERHOST_HELPER"
	floodLines = 20000
)

func TestMain(m *testing.M) {
	if mode := os.Getenv(helperEnv); mode != "" {
		runHelper(mode)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func runHelper(mode string) {
	switch mode {
	case "peer":
		args, _ := json.Marshal(os.Args[1:])
		fmt.Fprintf(os.Stderr, "args=%s\ninjected=%s\ninherited=%s\n", args, os.Getenv("ADAPTERHOST_INJECTED"), os.Getenv("ADAPTERHOST_INHERITED"))
		p, err := peer.New(os.Stdin, os.Stdout, peer.Config{})
		if err != nil {
			os.Exit(2)
		}
		p.Handle(adapterproto.MethodPing, func(context.Context, json.RawMessage) (any, error) { return adapterproto.Empty{}, nil })
		p.Start()
		<-p.Done()
	case "flood":
		w := bufio.NewWriter(os.Stdout)
		for i := range floodLines {
			fmt.Fprintf(w, "{\"n\":%d}\n", i)
		}
		_ = w.Flush()
		// Exits at once, with everything written: what is in the pipe must still be read.
	case "hang":
		fmt.Fprintln(os.Stderr, "ready")
		select {}
	}
}

// syncBuffer is a writer the launcher's copying goroutine and the test share.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// helperInstance is an installed package whose program is the test binary, at the
// path the manifest names under the directory of the instance.
func helperInstance(t *testing.T, args []string, stderr io.Writer) adapterhost.Instance {
	t.Helper()
	dir := t.TempDir()
	program := "bin/helper"
	if err := os.MkdirAll(filepath.Join(dir, "bin"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(testBinary(), filepath.Join(dir, "bin", "helper")); err != nil {
		// A file system without symbolic links: start the binary where it is.
		dir, program = filepath.Dir(testBinary()), filepath.Base(testBinary())
	}
	return adapterhost.Instance{
		ID: "chat",
		Adapter: config.Adapter{ID: "fake", Version: "0.1.0", Manifest: manifest.Manifest{
			Entry: manifest.Entry{Program: program, Args: args},
		}},
		Dir:      dir,
		LockPath: filepath.Join(dir, "adapter-chat.lock"),
		Stderr:   stderr,
	}
}

func launcherFor(mode string) adapterhost.ExecLauncher {
	return adapterhost.ExecLauncher{Env: []string{helperEnv + "=" + mode, "ADAPTERHOST_INJECTED=yes"}}
}

// The launcher runs the program the manifest names, with the manifest's arguments,
// the process's own environment plus its own, over standard streams: standard
// error goes to the instance's writer, standard output is the protocol stream and
// loses nothing at the end, and Kill is idempotent.
func TestExecLauncherRunsTheProgramOfTheManifestOverStandardStreams(t *testing.T) {
	t.Setenv("ADAPTERHOST_INHERITED", "from-the-daemon")

	t.Run("program, arguments, environment, protocol and standard error", func(t *testing.T) {
		stderr := &syncBuffer{}
		inst := helperInstance(t, []string{"--stdio", "two words"}, stderr)
		proc, err := launcherFor("peer").Launch(context.Background(), inst)
		if err != nil {
			t.Fatalf("Launch: %v", err)
		}
		if proc.PID() <= 0 {
			t.Fatalf("PID = %d, want a process id", proc.PID())
		}
		r, w := proc.Stream()
		client, err := peer.New(r, w, peer.Config{})
		if err != nil {
			t.Fatal(err)
		}
		client.Start()
		var pong adapterproto.Empty
		if err := client.Call(timeoutCtx(t), adapterproto.MethodPing, adapterproto.Empty{}, &pong); err != nil {
			t.Fatalf("ping over the standard streams: %v", err)
		}
		// Closing the stream is how the program is told to end.
		if err := client.Close(); err != nil {
			t.Logf("Close: %v", err)
		}
		if err := proc.Wait(); err != nil {
			t.Fatalf("Wait = %v, want the program to end without error once its input closed", err)
		}
		for _, want := range []string{`args=["--stdio","two words"]`, "injected=yes", "inherited=from-the-daemon"} {
			if !strings.Contains(stderr.String(), want+"\n") {
				t.Errorf("standard error lacks %q: %q", want, stderr.String())
			}
		}
		// Killing a process that has ended is not an error, and neither is doing it twice.
		for range 2 {
			if err := proc.Kill(); err != nil {
				t.Errorf("Kill after the end: %v", err)
			}
		}
	})

	t.Run("nothing written before the end is lost", func(t *testing.T) {
		inst := helperInstance(t, nil, io.Discard)
		proc, err := launcherFor("flood").Launch(context.Background(), inst)
		if err != nil {
			t.Fatal(err)
		}
		r, _ := proc.Stream()
		br := bufio.NewReader(r)
		n := 0
		for {
			line, err := br.ReadString('\n')
			if err == io.EOF {
				if line != "" {
					t.Fatalf("a partial line at the end: %q", line)
				}
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			if want := fmt.Sprintf("{\"n\":%d}\n", n); line != want {
				t.Fatalf("line %d = %q, want %q", n, line, want)
			}
			n++
		}
		if n != floodLines {
			t.Fatalf("read %d lines, want %d: output was lost at the end", n, floodLines)
		}
		if err := proc.Wait(); err != nil {
			t.Fatalf("Wait = %v", err)
		}
	})

	t.Run("Kill ends a process and may be called again", func(t *testing.T) {
		stderr := &syncBuffer{}
		inst := helperInstance(t, nil, stderr)
		proc, err := launcherFor("hang").Launch(context.Background(), inst)
		if err != nil {
			t.Fatal(err)
		}
		eventually(t, "the program to start", func() bool { return strings.Contains(stderr.String(), "ready") })
		for range 2 {
			if err := proc.Kill(); err != nil {
				t.Errorf("Kill: %v", err)
			}
		}
		if err := proc.Wait(); err == nil {
			t.Error("Wait of a killed process returned no error")
		}
		if err := proc.Kill(); err != nil {
			t.Errorf("Kill after Wait: %v", err)
		}
	})

	t.Run("a program that is not there is an error of Launch", func(t *testing.T) {
		inst := helperInstance(t, nil, io.Discard)
		inst.Adapter.Manifest.Entry.Program = "bin/not-there"
		if _, err := launcherFor("peer").Launch(context.Background(), inst); err == nil {
			t.Fatal("Launch of a program that is not there succeeded")
		}
	})
}
