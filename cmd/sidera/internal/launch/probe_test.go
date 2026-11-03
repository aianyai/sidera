package launch

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// The tests run their own binary as the program to probe; the mode names what it
// pretends to be.
const childEnv = "SIDERA_LAUNCH_TEST_CHILD"

func TestMain(m *testing.M) {
	if mode := os.Getenv(childEnv); mode != "" {
		child(mode)
	}
	os.Exit(m.Run())
}

func child(mode string) {
	// The program answers only the command line a probe is meant to send; in
	// particular the name siderad must not reach it.
	if strings.Join(os.Args, " ") != "sidera version --json" {
		fmt.Fprintf(os.Stderr, "unexpected command line %q\n", os.Args)
		os.Exit(3)
	}
	switch mode {
	case "ok":
		fmt.Println(`{"version":"1.2.3","commit":"abc","modified":false,"go_version":"go1.26","distribution":"unknown"}`)
	case "text":
		fmt.Println("sidera 1.2.3 (go1.26)")
	case "noversion":
		fmt.Println(`{"modified":false}`)
	case "fail":
		fmt.Fprintln(os.Stderr, "no node here")
		os.Exit(1)
	case "hang":
		time.Sleep(time.Minute)
	}
	os.Exit(0)
}

func TestProbeRunsVersionUnderTheNameSidera(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name    string
		mode    string
		arg0    string
		timeout time.Duration
		want    string // the version, or the text the error contains
		wantErr bool
	}{
		{"reports the version", "ok", "sidera", time.Minute, "1.2.3", false},
		{"started as siderad", "ok", "siderad", time.Minute, "1.2.3", false},
		{"not JSON", "text", "sidera", time.Minute, "did not print a version object", true},
		{"no version", "noversion", "sidera", time.Minute, "reported no version", true},
		{"fails", "fail", "sidera", time.Minute, "no node here", true},
		{"does not answer", "hang", "sidera", 300 * time.Millisecond, "deadline exceeded", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(childEnv, tc.mode)
			ctx, cancel := context.WithTimeout(context.Background(), tc.timeout)
			defer cancel()
			started := time.Now()
			info, err := Launch{Program: exe, Args: []string{tc.arg0, "daemon"}}.Probe(ctx)
			if tc.wantErr {
				if err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("Probe error = %v, want one containing %q", err, tc.want)
				}
				if time.Since(started) > 20*time.Second {
					t.Errorf("Probe took %v to give up", time.Since(started))
				}
				return
			}
			if err != nil || info.Version != tc.want {
				t.Fatalf("Probe = %+v, %v; want version %q", info, err, tc.want)
			}
		})
	}
}
