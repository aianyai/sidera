//go:build acceptance

package acceptance

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
	"time"
)

var testBinaries Binaries
var testRoot string
var fixtureNumber int

func TestMain(m *testing.M) {
	if diagnosticWCLHelperProcess() {
		return
	}
	start := time.Now().UTC()
	diagnosticInit()
	wd, err := os.Getwd()
	if err != nil {
		panic(err)
	}
	repo := filepath.Clean(filepath.Join(wd, "../../../.."))
	scratch := filepath.Join(repo, ".scratch", "bb")
	if filepath.Base(filepath.Dir(repo)) == ".worktrees" {
		scratch = filepath.Join(filepath.Dir(repo), ".scratch", "bb")
	}
	if err = os.MkdirAll(scratch, 0o700); err != nil {
		panic(err)
	}
	testRoot, err = os.MkdirTemp(scratch, "t")
	if err != nil {
		panic(err)
	}
	code := func() int {
		ext := ""
		if runtime.GOOS == "windows" {
			ext = ".exe"
		}
		jobs := []struct {
			name, path, tags string
			dest             *string
		}{
			{"tagged", "./cmd/sidera", "crashtest,testclock", &testBinaries.Tagged},
			{"plain", "./cmd/sidera", "", &testBinaries.Plain},
			{"adapter", "./cmd/sidera-fake-adapter", "", &testBinaries.Adapter},
			{"runtime", "./cmd/sidera-fake-runtime", "", &testBinaries.Runtime},
		}
		for _, job := range jobs {
			*job.dest = filepath.Join(testRoot, job.name+ext)
			args := []string{"build", "-o", *job.dest}
			if raceEnabled {
				args = append(args, "-race")
			}
			if job.tags != "" {
				args = append(args, "-tags", job.tags)
			}
			args = append(args, job.path)
			cmd := exec.Command("go", args...)
			cmd.Dir = repo
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			if err := cmd.Run(); err != nil {
				fmt.Fprintln(os.Stderr, err)
				return 1
			}
		}
		return m.Run()
	}()
	if err := os.RemoveAll(testRoot); err != nil {
		fmt.Fprintln(os.Stderr, err)
		code = 1
	}
	os.Exit(diagnosticFinish(start, code))
}

func runRoot(t *testing.T) string {
	t.Helper()
	return runRootAfterRemove(t, nil)
}

func runRootAfterRemove(t *testing.T, afterRemove func(string, error)) string {
	t.Helper()
	fixtureNumber++
	root := filepath.Join(testRoot, strconv.Itoa(fixtureNumber))
	err := os.Mkdir(root, 0o700)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		err := os.RemoveAll(root)
		if err != nil {
			diagnosticCleanup(root, "test-root", nil, nil, err)
			diagnosticCapture(root)
			t.Error(err)
		}
		if afterRemove != nil {
			afterRemove(root, err)
		}
	})
	return root
}
