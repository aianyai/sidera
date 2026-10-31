// This command belongs only to the disposable native diagnostic workflow.
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
)

const acceptancePackage = "github.com/aianyai/sidera/cmd/sidera/internal/acceptance"

var tests = map[string]string{
	"management":  "TestBinaryManagementFeedbackRepro",
	"delivery":    "TestRegisterReplyExactTargetRepro",
	"acceptance":  "TestUnobservedPreparationLeavesGenerationOneForDaemon",
	"maintenance": "TestManagementMaintenanceExactTargetRepro",
}

type event struct {
	Action, Package, Test, Output string
}

type attempt struct {
	output strings.Builder
	action string
}

type summary struct {
	Family         string `json:"family"`
	Expected       int    `json:"expected"`
	Same           int    `json:"same"`
	Green          int    `json:"green"`
	Other          int    `json:"other"`
	Incomplete     int    `json:"incomplete"`
	WaitCandidate  int    `json:"wait_candidate"`
	TargetUnproven int    `json:"target_unproven"`
	OtherWait      int    `json:"other_wait"`
}

var sharingViolation = regexp.MustCompile(`unlinkat [^\r\n]*[\\/]l[\\/]adapter_[^\\/\r\n]+\.jsonl: The process cannot access the file because it is being used by another process\.`)
var generationTwoStart = regexp.MustCompile(`\(\*Fixture\)\.Start\([^\n]*, \{0x2,`)

func sameSymptom(family, text string) bool {
	switch family {
	case "management":
		return strings.Contains(text, "phase=main target={Gen:1 Point:source-update/out.write#4 AfterStep:} trigger=<nil> actualReached=[true] generations=2") &&
			strings.Contains(text, "has no control reply with corresponding terminal feedback:") &&
			strings.Contains(text, "State:delivery_unknown") && strings.Contains(text, "ControlReply:<nil>") &&
			strings.Contains(text, "original_error=<nil> compare=<nil>")
	case "delivery":
		return strings.Contains(text, `step \"created-reply\": creation has no real created reply:`) &&
			strings.Contains(text, "State:accepted") && strings.Contains(text, "ControlReply:<nil>") &&
			strings.Contains(text, "REPRO result phase=main exact_symptom=true")
	case "acceptance":
		return sharingViolation.MatchString(text)
	}
	return false
}

func validPass(family, text string) bool {
	switch family {
	case "management", "maintenance":
		return strings.Contains(text, "actualReached=[true] generations=2 oracle=[] original_error=<nil> compare=<nil>")
	case "delivery":
		return strings.Contains(text, `phase=main exact_symptom=false error="<nil>" reached=[true] generations=2 violations=[]`) && strings.Contains(text, "REPRO compare=PASS")
	case "acceptance":
		return true // The original test's assertions are the contract.
	}
	return false
}

// All three frames must belong to the test goroutine, not separate stacks.
func maintenanceWait(text string) string {
	main := strings.LastIndex(text, "REPRO start seed=TestCrashAtEveryCrashPointPreservesInvariants scenario=management-maintenance target={Gen:1 Point:clock-high-water/commit#2 AfterStep:} phase=main")
	if main < 0 {
		return "target_unproven"
	}
	text = text[main:]
	for _, stack := range strings.Split(text, "\ngoroutine ") {
		if !strings.Contains(stack, "TestManagementMaintenanceExactTargetRepro(") {
			continue
		}
		a := strings.Index(stack, "(*Fixture).waitProbes(")
		b := strings.Index(stack, "(*Fixture).Start(")
		c := strings.Index(stack, "(*binaryRun).runScript(")
		if a >= 0 && b > a && c > b {
			if generationTwoStart.MatchString(stack) {
				return "wait_candidate"
			}
			return "target_unproven"
		}
		return "other_wait"
	}
	return "target_unproven"
}

func classify(r io.Reader, out io.Writer, family string, expected int) (summary, error) {
	s := summary{Family: family, Expected: expected}
	var attempts []*attempt
	var current *attempt
	var all strings.Builder
	packageEnd := ""
	reader := bufio.NewReader(r)
	invalid := false
	for {
		line, readErr := reader.ReadString('\n')
		if len(line) == 0 && readErr == io.EOF {
			break
		}
		var e event
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			// Build/setup diagnostics can be plain stderr. Keep them in the
			// console, but never let an unparsed stream qualify a batch.
			fmt.Fprint(out, line)
			invalid = true
		} else {
			fmt.Fprint(out, e.Output)
			if e.Package == acceptancePackage {
				all.WriteString(e.Output)
				if e.Test == tests[family] && e.Action == "run" {
					current = &attempt{}
					attempts = append(attempts, current)
				}
				if current != nil {
					current.output.WriteString(e.Output)
					if e.Test == tests[family] && (e.Action == "pass" || e.Action == "fail" || e.Action == "skip") {
						current.action = e.Action
						current = nil
					}
				}
				if e.Test == "" && (e.Action == "pass" || e.Action == "fail") {
					packageEnd = e.Action
				}
			}
		}
		if readErr != nil {
			if readErr != io.EOF {
				return s, readErr
			}
			break
		}
	}
	if invalid {
		return s, fmt.Errorf("unparsed go test output; batch cannot qualify")
	}
	if len(attempts) > expected {
		return s, fmt.Errorf("more tests than the fixed budget")
	}
	text := all.String()
	timedOut := strings.Contains(text, "panic: test timed out after ")
	hasFailure := false
	for _, a := range attempts {
		hasFailure = hasFailure || a.action == "fail" || a.action == ""
	}
	unexplainedPackageFailure := packageEnd == "fail" && !hasFailure
	for i, a := range attempts {
		body := a.output.String()
		classification := "other"
		switch {
		case packageEnd == "" || unexplainedPackageFailure || a.action == "" || (timedOut && i == len(attempts)-1):
			s.Incomplete++
			classification = "incomplete"
			if family == "maintenance" && timedOut {
				detail := maintenanceWait(body)
				switch detail {
				case "wait_candidate":
					s.WaitCandidate++
				case "target_unproven":
					s.TargetUnproven++
				case "other_wait":
					s.OtherWait++
				}
				reached := "unknown"
				if strings.Contains(body, "actualReached=[true]") {
					reached = "true"
				} else if strings.Contains(body, "actualReached=[false]") {
					reached = "false"
				}
				fmt.Fprintf(out, "NATIVE maintenance timeout=%s reached=%s; a candidate identifies only a waiting boundary\n", detail, reached)
			}
		case a.action == "fail" && sameSymptom(family, body):
			s.Same++
			classification = "same"
		case a.action == "pass" && validPass(family, body):
			s.Green++
			classification = "green"
		default:
			s.Other++
		}
		fmt.Fprintf(out, "NATIVE attempt=%d classification=%s\n", i+1, classification)
	}
	s.Incomplete += expected - len(attempts)
	return s, nil
}

func main() {
	family := flag.String("family", "", "fixed diagnostic family")
	count := flag.Int("count", 0, "fixed sample count")
	flag.Parse()
	if tests[*family] == "" || (*count != 10 && *count != 20) {
		fmt.Fprintln(os.Stderr, "invalid fixed batch")
		os.Exit(2)
	}
	s, err := classify(os.Stdin, os.Stdout, *family, *count)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	data, _ := json.Marshal(s)
	fmt.Printf("NATIVE summary=%s\n", data)
	if path := os.Getenv("GITHUB_OUTPUT"); path != "" {
		f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		_, writeErr := fmt.Fprintf(f, "qualifies=%t\nsame=%d\ngreen=%d\nother=%d\nincomplete=%d\n", s.Same >= *count/2 && s.Incomplete == 0, s.Same, s.Green, s.Other, s.Incomplete)
		closeErr := f.Close()
		if writeErr != nil || closeErr != nil {
			fmt.Fprintln(os.Stderr, "cannot write batch counts")
			os.Exit(1)
		}
	}
}
