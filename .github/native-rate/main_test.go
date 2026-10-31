package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// These synthetic events retain the observed native output signatures while
// replacing paths and identifiers. No raw diagnostic log is checked in.
const managementFailure = `seed=TestCrashAtEveryCrashPointPreservesInvariants scenario=management-instance phase=main target={Gen:1 Point:source-update/out.write#4 AfterStep:} trigger=<nil> actualReached=[true] generations=2 oracle=[creation message has no control reply with corresponding terminal feedback: {Recipients:[{To:endpoint State:delivery_unknown Reason:}] ControlReply:<nil>}] original_error=<nil> compare=<nil>`
const deliveryFailure = `REPRO result phase=main exact_symptom=true error="step \"created-reply\": creation has no real created reply: {Recipients:[{To:endpoint State:accepted Reason:}] ControlReply:<nil>}\nacceptance: target clock-high-water/clock.rename#2 was not reached\ninvalid argument" reached=[false] generations=0 violations=[]`
const acceptanceFailure = `main_test.go:93: unlinkat D:\a\sidera\sidera\.scratch\bb\t123\9\l\adapter_fake-8960.jsonl: The process cannot access the file because it is being used by another process.`
const maintenanceStart = "REPRO start seed=TestCrashAtEveryCrashPointPreservesInvariants scenario=management-maintenance target={Gen:1 Point:clock-high-water/commit#2 AfterStep:} phase="
const maintenanceStack = `panic: test timed out after 1m30s

goroutine 18 [select]:
github.com/aianyai/sidera/cmd/sidera/internal/acceptance.(*Fixture).waitProbes(0x123, {0x456, 0x789})
    control.go:91 +0x20f
github.com/aianyai/sidera/cmd/sidera/internal/acceptance.(*Fixture).Start(0x123, {0x456, 0x789}, {0x2, {0x0, 0x0}, {0x0, 0x0}})
    fixture.go:286 +0x49c
github.com/aianyai/sidera/cmd/sidera/internal/acceptance.(*binaryRun).runScript(0x123, {0x456, 0x789}, 0xabc)
    walk.go:278 +0x936
github.com/aianyai/sidera/cmd/sidera/internal/acceptance.TestManagementMaintenanceExactTargetRepro(0x123)
    management_maintenance_repro_test.go:32
`

func stream(family, body, terminal, packageEnd string) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	for _, e := range []event{
		{Action: "run", Package: acceptancePackage, Test: tests[family]},
		{Action: "output", Package: acceptancePackage, Test: tests[family], Output: body},
		{Action: terminal, Package: acceptancePackage, Test: tests[family]},
		{Action: packageEnd, Package: acceptancePackage},
	} {
		if e.Action != "" {
			_ = enc.Encode(e)
		}
	}
	return b.String()
}

func TestAttemptClassification(t *testing.T) {
	for _, tc := range []struct {
		name, family, body, terminal, packageEnd                       string
		same, green, other, incomplete, candidate, unproven, otherWait int
	}{
		{name: "management original", family: "management", body: managementFailure, terminal: "fail", packageEnd: "fail", same: 1},
		{name: "management different state", family: "management", body: strings.ReplaceAll(managementFailure, "State:delivery_unknown", "State:expired"), terminal: "fail", packageEnd: "fail", other: 1},
		{name: "delivery original", family: "delivery", body: deliveryFailure, terminal: "fail", packageEnd: "fail", same: 1},
		{name: "delivery nil reply alone", family: "delivery", body: "ControlReply:<nil>", terminal: "fail", packageEnd: "fail", other: 1},
		{name: "acceptance original", family: "acceptance", body: acceptanceFailure, terminal: "fail", packageEnd: "fail", same: 1},
		{name: "unrelated file", family: "acceptance", body: strings.ReplaceAll(acceptanceFailure, "adapter_fake-8960.jsonl", "runtime-8960.jsonl"), terminal: "fail", packageEnd: "fail", other: 1},
		{name: "different cleanup error", family: "acceptance", body: "unlinkat adapter_fake.jsonl: access denied", terminal: "fail", packageEnd: "fail", other: 1},
		{name: "acceptance green", family: "acceptance", terminal: "pass", packageEnd: "pass", green: 1},
		{name: "target evidence missing", family: "management", terminal: "pass", packageEnd: "pass", other: 1},
		{name: "management green", family: "management", body: "actualReached=[true] generations=2 oracle=[] original_error=<nil> compare=<nil>", terminal: "pass", packageEnd: "pass", green: 1},
		{name: "delivery green", family: "delivery", body: "REPRO result phase=main exact_symptom=false error=\"<nil>\" reached=[true] generations=2 violations=[]\nREPRO compare=PASS", terminal: "pass", packageEnd: "pass", green: 1},
		{name: "teardown failure", family: "acceptance", terminal: "pass", packageEnd: "fail", incomplete: 1},
		{name: "failed test without package result", family: "management", body: managementFailure, terminal: "fail", incomplete: 1},
		{name: "no package result", family: "acceptance", terminal: "pass", incomplete: 1},
		{name: "skipped", family: "acceptance", terminal: "skip", packageEnd: "pass", other: 1},
		{name: "maintenance candidate", family: "maintenance", body: maintenanceStart + "main\n" + maintenanceStack, packageEnd: "fail", incomplete: 1, candidate: 1},
		{name: "maintenance clean wait", family: "maintenance", body: maintenanceStart + "clean\n" + maintenanceStack, packageEnd: "fail", incomplete: 1, unproven: 1},
		{name: "maintenance generation one", family: "maintenance", body: maintenanceStart + "main\n" + strings.ReplaceAll(maintenanceStack, "{0x2,", "{0x1,"), packageEnd: "fail", incomplete: 1, unproven: 1},
		{name: "maintenance different stack", family: "maintenance", body: maintenanceStart + "main\n" + strings.ReplaceAll(maintenanceStack, "(*Fixture).waitProbes(", "(*Fixture).Close("), packageEnd: "fail", incomplete: 1, otherWait: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := classify(strings.NewReader(stream(tc.family, tc.body, tc.terminal, tc.packageEnd)), &bytes.Buffer{}, tc.family, 1)
			if err != nil {
				t.Fatal(err)
			}
			want := summary{Family: tc.family, Expected: 1, Same: tc.same, Green: tc.green, Other: tc.other, Incomplete: tc.incomplete, WaitCandidate: tc.candidate, TargetUnproven: tc.unproven, OtherWait: tc.otherWait}
			if got != want {
				t.Fatalf("got %+v; want %+v", got, want)
			}
		})
	}
}

func TestFixedBatchCountsMissingSamplesAndKeepsConsoleOutput(t *testing.T) {
	input := stream("acceptance", acceptanceFailure+"\n", "fail", "fail")
	var console bytes.Buffer
	got, err := classify(strings.NewReader(input), &console, "acceptance", 20)
	if err != nil {
		t.Fatal(err)
	}
	if got.Same != 1 || got.Incomplete != 19 || got.Green != 0 {
		t.Fatalf("unexpected counts: %+v", got)
	}
	if !strings.Contains(console.String(), acceptanceFailure+"\n") {
		t.Fatal("original console output was lost")
	}
	got, err = classify(strings.NewReader(""), &console, "acceptance", 20)
	if err != nil || got.Incomplete != 20 {
		t.Fatalf("missing test: %+v %v", got, err)
	}
	console.Reset()
	if _, err := classify(strings.NewReader("build failed\n"), &console, "acceptance", 20); err == nil || console.String() != "build failed\n" {
		t.Fatalf("setup failure: %v %q", err, console.String())
	}
}
