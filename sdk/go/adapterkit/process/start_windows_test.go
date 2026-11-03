package process_test

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"testing"
	"unsafe"

	"github.com/aianyai/sidera/sdk/go/adapterkit/process"
	"golang.org/x/sys/windows"
)

// The job the adapter runs in decides how far a started process escapes: the
// daemon may put the adapter in a job that forbids breakaway, and one that ends
// its processes when it closes. These tests make the test binary an adapter in
// such a job and read what its Start says and what happened to the process.

// jobReport is what the adapter child tells.
type jobReport struct {
	Host, Started process.Detachment
	PID           int
}

func init() {
	childModes["adapter"] = func() {
		in := bufio.NewReader(os.Stdin)
		_, _ = in.ReadString('\n') // the test has put this process in its job
		exe, _ := os.Executable()
		var r jobReport
		r.Host = process.HostDetachment()
		started, err := process.Start(process.Spec{Argv: []string{exe}, Env: childEnvironment("hold")})
		if err != nil {
			println("start:", err.Error())
			os.Exit(1)
		}
		r.Started, r.PID = started.Detachment, started.PID
		_ = json.NewEncoder(os.Stdout).Encode(r)
		_, _ = io.Copy(io.Discard, in) // until the test is done
		_ = started.Kill()
	}
}

func TestStartInsideAJob(t *testing.T) {
	for _, tc := range []struct {
		name       string
		limits     uint32
		outlives   bool
		hasCaveat  bool
		grandchild bool // stays in the job
	}{
		{"breakaway forbidden", 0, true, true, true},
		{"breakaway forbidden, kill on close", windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE, false, true, true},
		{"breakaway allowed", windows.JOB_OBJECT_LIMIT_BREAKAWAY_OK, true, false, false},
		{"breakaway allowed, kill on close", windows.JOB_OBJECT_LIMIT_BREAKAWAY_OK | windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE, true, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			job, err := windows.CreateJobObject(nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer windows.CloseHandle(job) // ends what is left in a kill-on-close job
			var info windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
			info.BasicLimitInformation.LimitFlags = tc.limits
			if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
				uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
				t.Fatal(err)
			}

			exe, _ := os.Executable()
			adapter := exec.Command(exe)
			adapter.Env = childEnvironment("adapter")
			stdin, _ := adapter.StdinPipe()
			stdout, _ := adapter.StdoutPipe()
			if err := adapter.Start(); err != nil {
				t.Fatal(err)
			}
			// The adapter ends the process it started when its input ends. Only a
			// failed test kills it instead, and then the started process outlives it
			// until its own lifetime is up.
			ended := false
			defer func() {
				if !ended {
					_ = adapter.Process.Kill()
					_ = adapter.Wait()
				}
			}()

			h, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(adapter.Process.Pid))
			if err != nil {
				t.Fatal(err)
			}
			err = windows.AssignProcessToJobObject(job, h)
			windows.CloseHandle(h)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := io.WriteString(stdin, "go\n"); err != nil {
				t.Fatal(err)
			}
			var r jobReport
			if err := json.NewDecoder(stdout).Decode(&r); err != nil {
				t.Fatalf("the adapter did not report: %v", err)
			}

			if r.Started.OutlivesDaemon != tc.outlives || (r.Started.Caveat != "") != tc.hasCaveat {
				t.Errorf("Start says %+v, want OutlivesDaemon %v and a caveat %v", r.Started, tc.outlives, tc.hasCaveat)
			}
			if r.Host != r.Started {
				t.Errorf("HostDetachment says %+v, Start says %+v", r.Host, r.Started)
			}
			if got := inJob(t, r.PID, job); got != tc.grandchild {
				t.Errorf("the started process is in the job: %v, want %v", got, tc.grandchild)
			}
			_ = stdin.Close()
			ended = adapter.Wait() == nil
		})
	}
}

func inJob(t *testing.T, pid int, job windows.Handle) bool {
	t.Helper()
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		t.Fatalf("open the started process: %v", err)
	}
	defer windows.CloseHandle(h)
	var in int32
	proc := windows.NewLazySystemDLL("kernel32.dll").NewProc("IsProcessInJob")
	if r, _, err := proc.Call(uintptr(h), uintptr(job), uintptr(unsafe.Pointer(&in))); r == 0 {
		t.Fatalf("IsProcessInJob: %v", err)
	}
	return in != 0
}
