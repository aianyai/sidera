package engine_test

import (
	"reflect"
	"sort"
	"testing"

	"github.com/aianyai/sidera/internal/node/adapterhost"
	"github.com/aianyai/sidera/internal/node/engine"
	"github.com/aianyai/sidera/internal/node/store"
)

// A session endpoint comes only from a session creation: the engine has no
// method that registers one, and a method that took or gave a record of one
// would be the beginning of such a method. The list of the methods is the point
// of review: a new method is added to it by a change that says what it is for.
func TestEngineHasNoMethodThatMakesASessionEndpoint(t *testing.T) {
	allowed := []string{
		"Ack", "Adapter", "AdapterAdmitted", "AdapterGone", "AdapterList", "AdapterNotified", "AdapterProbed", "AdapterSignals", "AdapterStarted",
		"AddArchiveSweep", "AddClockState", "AddPeriodic", "Alerts", "BeginShutdown", "CheckpointPut", "CloseEndpoint", "Control", "Declare", "Done",
		"Endpoints", "Err", "Execute", "ExportRevocation", "Inbound", "MessageStatus", "NodeSettings", "OperationResult", "Phase", "PresentProbeToken", "Presets", "ProbeTokens", "Pull", "Quiesce", "RaiseAlert", "RecoverDelivery",
		"Recv", "RecvObserved", "RegisterDirect", "Reply", "Send", "Serve", "SetTransport", "Start", "Status", "Stop", "SystemAddress", "UnregisterDirect",
	}
	typ := reflect.TypeOf(&engine.Engine{})
	var got []string
	banned := map[reflect.Type]string{
		reflect.TypeOf(store.EndpointRecord{}): "store.EndpointRecord",
		reflect.TypeOf(store.SessionInfo{}):    "store.SessionInfo",
		reflect.TypeOf(store.CtlRecord{}):      "store.CtlRecord",
	}
	for i := 0; i < typ.NumMethod(); i++ {
		m := typ.Method(i)
		got = append(got, m.Name)
		for j := 0; j < m.Type.NumIn(); j++ {
			if name, bad := banned[m.Type.In(j)]; bad {
				t.Errorf("%s takes a %s", m.Name, name)
			}
		}
		for j := 0; j < m.Type.NumOut(); j++ {
			if name, bad := banned[m.Type.Out(j)]; bad {
				t.Errorf("%s gives a %s", m.Name, name)
			}
		}
	}
	sort.Strings(got)
	sort.Strings(allowed)
	if !reflect.DeepEqual(got, allowed) {
		t.Errorf("the methods of the engine are\n%v\nthe reviewed list is\n%v", got, allowed)
	}
}

// The loop and the worker that stages an adapter package never write under one
// transaction name, so that the crash points of each are numbered by one
// goroutine and a run can be replayed.
func TestTransactionsOfTheLoopAndOfTheInstallWorkerAreDisjoint(t *testing.T) {
	installWorker := map[string]bool{"adapter-stage": true, "adapter-unstage": true}
	for _, d := range engine.Transactions() {
		if installWorker[d.Name] {
			t.Errorf("engine.Transactions holds %q, which belongs to the install worker", d.Name)
		}
	}
}

// The transactions of two owners that the engine opens are these and no others.
func TestTheEngineDeclaresTheTransactionsOfTwoOwnersItOpens(t *testing.T) {
	var got []string
	for _, d := range engine.Transactions() {
		got = append(got, d.Name)
	}
	sort.Strings(got)
	if want := []string{"adapter-install", "adapter-install-current", "backup-confirm", "clock-clear", "direct-register", "identity-retire", "runtime-admit"}; !reflect.DeepEqual(got, want) {
		t.Errorf("engine.Transactions = %v, want %v", got, want)
	}
}

// The engine is what the adapter host asks its questions of: the interface is the
// host's, and the engine satisfies it.
func TestTheEngineIsTheEngineTheAdapterHostAsksOf(t *testing.T) {
	var _ adapterhost.Engine = (*engine.Engine)(nil)
}

func TestTheEnginesTransactionsAreInTheTableOfTheProgram(t *testing.T) {
	tab := programTable(t)
	names := map[string]bool{}
	for _, k := range tab.Keys() {
		names[k.Tag.Tx] = true
	}
	for _, d := range engine.Transactions() {
		if !names[d.Name] {
			t.Errorf("no crash point of %q is in the table", d.Name)
		}
	}
}

// The engine asks the adapter host for these things and no others; a new call is
// added to the list by a change that says what it is for.
func TestAdaptersListsWhatTheEngineAsksOfTheAdapterHost(t *testing.T) {
	want := []string{"Close", "Counts", "CreateSession", "DeliverPrompt", "EnsureRunning", "Interrupt", "NotifyPending", "Reconcile", "Stop", "Stopping"}
	typ := reflect.TypeOf((*engine.Adapters)(nil)).Elem()
	var got []string
	for i := 0; i < typ.NumMethod(); i++ {
		got = append(got, typ.Method(i).Name)
	}
	sort.Strings(got)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("the methods are %v, want %v", got, want)
	}
}
