package admin

import (
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/authz"
)

// catalogue is the complete operation catalogue with the requirement each
// entry must have. The test below fails if an OpKind is added without a row.
var catalogue = []struct {
	name string
	op   Operation
	want Requirement
}{
	// Reading needs view.
	{"read state, policy, endpoints, logs and diagnostics needs view", Operation{Kind: OpRead}, RequireView},
	{"querying the result of an operation needs view", Operation{Kind: OpQueryOperationResult}, RequireView},

	// Withdrawing and tightening need manage.
	{"revoke trust needs manage", Operation{Kind: OpRevokeTrust}, RequireManage},
	{"locally revoking a node needs manage", Operation{Kind: OpLocallyRevokeNode}, RequireManage},
	{"rejecting a trust request needs manage", Operation{Kind: OpRejectTrustRequest}, RequireManage},
	{"disabling inheritance needs manage", Operation{Kind: OpDisableInheritance}, RequireManage},
	{"shortening the grace period needs manage", Operation{Kind: OpShortenGracePeriod}, RequireManage},
	{"tightening policy needs manage", Operation{Kind: OpChangePolicy, PolicyChange: authz.Tighten}, RequireManage},
	{"a policy change that moves nothing needs manage", Operation{Kind: OpChangePolicy, PolicyChange: authz.Unchanged}, RequireManage},
	{"managing endpoints and sessions needs manage", Operation{Kind: OpManageEndpointSession}, RequireManage},
	{"editing the declaration needs manage", Operation{Kind: OpEditDeclaration}, RequireManage},
	{"changing relay settings needs manage", Operation{Kind: OpChangeRelaySettings}, RequireManage},
	{"switching LAN discovery on or off needs manage", Operation{Kind: OpSetLANDiscovery}, RequireManage},
	{"removing or narrowing a preset needs manage", Operation{Kind: OpChangePreset, PresetChange: PresetChangeNarrowing}, RequireManage},
	{"issuing an offline directive needs manage", Operation{Kind: OpIssueOfflineDirective}, RequireManage},

	// Letting more nodes in or back, running more code and loosening need full control.
	{"confirming trust establishment needs full control", Operation{Kind: OpConfirmTrust}, RequireFullControl},
	{"enabling inheritance needs full control", Operation{Kind: OpEnableInheritance}, RequireFullControl},
	{"extending the grace period needs full control", Operation{Kind: OpExtendGracePeriod}, RequireFullControl},
	{"clearing a locally-revoked record needs full control", Operation{Kind: OpClearLocallyRevoked}, RequireFullControl},
	{"installing or upgrading an adapter needs full control", Operation{Kind: OpInstallAdapter}, RequireFullControl},
	{"adding or loosening a preset needs full control", Operation{Kind: OpChangePreset, PresetChange: PresetChangeLoosening}, RequireFullControl},
	{"an unclassifiable preset change counts as loosening", Operation{Kind: OpChangePreset, PresetChange: PresetChangeUnclassified}, RequireFullControl},
	{"an out-of-range preset classification counts as loosening", Operation{Kind: OpChangePreset, PresetChange: 99}, RequireFullControl},
	{"loosening policy needs full control", Operation{Kind: OpChangePolicy, PolicyChange: authz.Loosen}, RequireFullControl},
	{"a policy change with the classification unset (zero value) needs full control", Operation{Kind: OpChangePolicy}, RequireFullControl},
	{"an out-of-range policy classification needs full control", Operation{Kind: OpChangePolicy, PolicyChange: 99}, RequireFullControl},

	// The list itself belongs to the local owner.
	{"modifying the authorized-persons list is for the local owner only", Operation{Kind: OpModifyAuthorizedList}, RequireLocalOwner},
}

func TestRequirementOf(t *testing.T) {
	for _, tc := range catalogue {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := RequirementOf(tc.op)
			if !ok || got != tc.want {
				t.Fatalf("RequirementOf = %v, %v; want %v, true", got, ok, tc.want)
			}
		})
	}
}

func TestCatalogueCoversEveryOperation(t *testing.T) {
	covered := map[OpKind]bool{}
	for _, tc := range catalogue {
		covered[tc.op.Kind] = true
	}
	for k := OpUnknown + 1; k <= OpModifyAuthorizedList; k++ {
		if !covered[k] {
			t.Errorf("operation kind %d has no catalogue row", k)
		}
	}
}

func TestUnknownOperationIsDeniedToEveryone(t *testing.T) {
	a := person(t, 1)
	list := mustList(t, Entry{Person: a, Level: LevelFullControl})
	for _, kind := range []OpKind{OpUnknown, OpModifyAuthorizedList + 1, -1} {
		for name, actor := range map[string]Actor{
			"local owner":  LocalOwner(),
			"full control": RemotePerson(a, validCert(LevelFullControl)),
		} {
			t.Run(name, func(t *testing.T) {
				v := Authorize(list, actor, Operation{Kind: kind}, t0)
				if v.Allowed || v.Reason != ReasonUnknownOperation {
					t.Fatalf("verdict = %+v, want denied as unknown operation", v)
				}
			})
		}
	}
}

func TestZeroActorHasNoRights(t *testing.T) {
	v := Authorize(List{}, Actor{}, Operation{Kind: OpRead}, t0)
	if v.Allowed || v.Reason != ReasonUnknownActor {
		t.Fatalf("verdict = %+v, want denied as unknown actor", v)
	}
}

// TestAuthorizeEveryOperationAtEveryLevel checks the whole matrix: the local
// owner may do everything; a remote person may do an operation exactly when
// their effective level reaches the requirement; no remote person ever
// reaches a local-owner-only operation. The expectation comes from the table
// below, not from the code under test.
func TestAuthorizeEveryOperationAtEveryLevel(t *testing.T) {
	a := person(t, 1)
	levels := []Level{LevelView, LevelManage, LevelFullControl}
	lowestLevel := map[Requirement]Level{
		RequireView:        LevelView,
		RequireManage:      LevelManage,
		RequireFullControl: LevelFullControl,
	}
	for _, tc := range catalogue {
		t.Run(tc.name+"/local owner", func(t *testing.T) {
			v := Authorize(List{}, LocalOwner(), tc.op, t0)
			if !v.Allowed || v.Reason != ReasonLocalOwner {
				t.Fatalf("verdict = %+v, want allowed for the local owner", v)
			}
		})
		for _, lvl := range levels {
			t.Run(tc.name+"/remote at "+lvl.String(), func(t *testing.T) {
				list := mustList(t, Entry{Person: a, Level: lvl})
				v := Authorize(list, RemotePerson(a, validCert(LevelFullControl)), tc.op, t0)
				need, listed := lowestLevel[tc.want]
				want := listed && lvl >= need
				if v.Allowed != want {
					t.Fatalf("allowed = %v (%s), want %v", v.Allowed, v.Reason, want)
				}
				if want && (v.Reason != ReasonOK || v.Level != lvl) {
					t.Fatalf("verdict = %+v, want ok at %v", v, lvl)
				}
				if !want {
					wantReason := ReasonListLevelTooLow
					if tc.want == RequireLocalOwner {
						wantReason = ReasonLocalOwnerOnly
					}
					if v.Reason != wantReason {
						t.Fatalf("reason = %s, want %s", v.Reason, wantReason)
					}
				}
			})
		}
	}
}

func TestEffectiveLevelAndAuthorizeRules(t *testing.T) {
	a, b := person(t, 1), person(t, 2)
	revokeTrust := Operation{Kind: OpRevokeTrust}
	installAdapter := Operation{Kind: OpInstallAdapter}
	read := Operation{Kind: OpRead}

	tests := []struct {
		name       string
		list       List
		actor      Actor
		op         Operation
		now        time.Time
		wantOK     bool
		wantReason Reason
		wantLevel  Level
	}{
		{
			name:   "effective level is the list level when the certificate cap is higher",
			list:   mustList(t, Entry{Person: a, Level: LevelManage}),
			actor:  RemotePerson(a, validCert(LevelFullControl)),
			op:     revokeTrust,
			now:    t0,
			wantOK: true, wantReason: ReasonOK, wantLevel: LevelManage,
		},
		{
			name:   "effective level is the certificate cap when the list level is higher",
			list:   mustList(t, Entry{Person: a, Level: LevelFullControl}),
			actor:  RemotePerson(a, validCert(LevelView)),
			op:     read,
			now:    t0,
			wantOK: true, wantReason: ReasonOK, wantLevel: LevelView,
		},
		{
			name:       "a view-capped certificate cannot manage even if the list says full control",
			list:       mustList(t, Entry{Person: a, Level: LevelFullControl}),
			actor:      RemotePerson(a, validCert(LevelView)),
			op:         revokeTrust,
			now:        t0,
			wantReason: ReasonDeviceCapTooLow, wantLevel: LevelView,
		},
		{
			name:       "a manage-capped certificate cannot install an adapter even if the list says full control",
			list:       mustList(t, Entry{Person: a, Level: LevelFullControl}),
			actor:      RemotePerson(a, validCert(LevelManage)),
			op:         installAdapter,
			now:        t0,
			wantReason: ReasonDeviceCapTooLow, wantLevel: LevelManage,
		},
		{
			name:       "a high certificate cap does not raise a low list level",
			list:       mustList(t, Entry{Person: a, Level: LevelView}),
			actor:      RemotePerson(a, validCert(LevelFullControl)),
			op:         revokeTrust,
			now:        t0,
			wantReason: ReasonListLevelTooLow, wantLevel: LevelView,
		},
		{
			name:       "a certificate with no usable cap grants nothing",
			list:       mustList(t, Entry{Person: a, Level: LevelFullControl}),
			actor:      RemotePerson(a, DeviceCert{NotBefore: t0.Add(-time.Hour), NotAfter: t0.Add(time.Hour)}),
			op:         read,
			now:        t0,
			wantReason: ReasonDeviceCapTooLow,
		},
		{
			name:       "a certificate cap outside the known levels grants nothing",
			list:       mustList(t, Entry{Person: a, Level: LevelFullControl}),
			actor:      RemotePerson(a, validCert(99)),
			op:         read,
			now:        t0,
			wantReason: ReasonDeviceCapTooLow,
		},
		{
			name:       "a person absent from this node's list has no rights, whatever they hold elsewhere",
			list:       mustList(t, Entry{Person: b, Level: LevelFullControl}),
			actor:      RemotePerson(a, validCert(LevelFullControl)),
			op:         read,
			now:        t0,
			wantReason: ReasonNotOnList,
		},
		{
			name:       "an empty list authorizes no remote person",
			list:       List{},
			actor:      RemotePerson(a, validCert(LevelFullControl)),
			op:         read,
			now:        t0,
			wantReason: ReasonNotOnList,
		},
		{
			name:       "an expired list entry grants nothing",
			list:       mustList(t, Entry{Person: a, Level: LevelFullControl, ExpiresAt: t0}),
			actor:      RemotePerson(a, validCert(LevelFullControl)),
			op:         read,
			now:        t0,
			wantReason: ReasonEntryExpired,
		},
		{
			name:   "an entry that expires later still grants its rights",
			list:   mustList(t, Entry{Person: a, Level: LevelManage, ExpiresAt: t0.Add(time.Second)}),
			actor:  RemotePerson(a, validCert(LevelFullControl)),
			op:     revokeTrust,
			now:    t0,
			wantOK: true, wantReason: ReasonOK, wantLevel: LevelManage,
		},
		{
			name: "a temporary subkey stops working when its certificate expires",
			list: mustList(t, Entry{Person: a, Level: LevelFullControl}),
			actor: RemotePerson(a, DeviceCert{
				MaxLevel: LevelFullControl, NotBefore: t0.Add(-2 * time.Hour), NotAfter: t0,
			}),
			op:         read,
			now:        t0,
			wantReason: ReasonCertExpired,
		},
		{
			name: "a temporary subkey works until its expiry",
			list: mustList(t, Entry{Person: a, Level: LevelFullControl}),
			actor: RemotePerson(a, DeviceCert{
				MaxLevel: LevelFullControl, NotBefore: t0.Add(-2 * time.Hour), NotAfter: t0.Add(time.Nanosecond),
			}),
			op:     read,
			now:    t0,
			wantOK: true, wantReason: ReasonOK, wantLevel: LevelFullControl,
		},
		{
			name: "a certificate with no expiry set counts as expired",
			list: mustList(t, Entry{Person: a, Level: LevelFullControl}),
			actor: RemotePerson(a, DeviceCert{
				MaxLevel: LevelFullControl, NotBefore: t0.Add(-time.Hour),
			}),
			op:         read,
			now:        t0,
			wantReason: ReasonCertExpired,
		},
		{
			name: "a certificate that is not valid yet grants nothing",
			list: mustList(t, Entry{Person: a, Level: LevelFullControl}),
			actor: RemotePerson(a, DeviceCert{
				MaxLevel: LevelFullControl, NotBefore: t0.Add(time.Second), NotAfter: t0.Add(time.Hour),
			}),
			op:         read,
			now:        t0,
			wantReason: ReasonCertNotYetValid,
		},
		{
			name: "a certificate is valid at the instant of its NotBefore",
			list: mustList(t, Entry{Person: a, Level: LevelFullControl}),
			actor: RemotePerson(a, DeviceCert{
				MaxLevel: LevelFullControl, NotBefore: t0, NotAfter: t0.Add(time.Hour),
			}),
			op:     read,
			now:    t0,
			wantOK: true, wantReason: ReasonOK, wantLevel: LevelFullControl,
		},
		{
			name: "a certificate is not valid one instant before its NotBefore",
			list: mustList(t, Entry{Person: a, Level: LevelFullControl}),
			actor: RemotePerson(a, DeviceCert{
				MaxLevel: LevelFullControl, NotBefore: t0.Add(time.Nanosecond), NotAfter: t0.Add(time.Hour),
			}),
			op:         read,
			now:        t0,
			wantReason: ReasonCertNotYetValid,
		},
		{
			name: "a revoked device certificate grants nothing",
			list: mustList(t, Entry{Person: a, Level: LevelFullControl}),
			actor: RemotePerson(a, DeviceCert{
				MaxLevel: LevelFullControl, NotBefore: t0.Add(-time.Hour), NotAfter: t0.Add(time.Hour), CertRevoked: true,
			}),
			op:         read,
			now:        t0,
			wantReason: ReasonCertRevoked,
		},
		{
			name: "a revoked root key grants nothing on any device",
			list: mustList(t, Entry{Person: a, Level: LevelFullControl}),
			actor: RemotePerson(a, DeviceCert{
				MaxLevel: LevelFullControl, NotBefore: t0.Add(-time.Hour), NotAfter: t0.Add(time.Hour), RootKeyRevoked: true,
			}),
			op:         read,
			now:        t0,
			wantReason: ReasonRootKeyRevoked,
		},
		{
			name:       "a remote full-control person cannot modify the authorized list",
			list:       mustList(t, Entry{Person: a, Level: LevelFullControl}),
			actor:      RemotePerson(a, validCert(LevelFullControl)),
			op:         Operation{Kind: OpModifyAuthorizedList},
			now:        t0,
			wantReason: ReasonLocalOwnerOnly,
		},
		{
			name:       "the list stays closed to a remote person even when they are not on it",
			list:       List{},
			actor:      RemotePerson(a, validCert(LevelFullControl)),
			op:         Operation{Kind: OpModifyAuthorizedList},
			now:        t0,
			wantReason: ReasonLocalOwnerOnly,
		},
		{
			name:   "the local owner may modify the authorized list, even on an empty list",
			list:   List{},
			actor:  LocalOwner(),
			op:     Operation{Kind: OpModifyAuthorizedList},
			now:    t0,
			wantOK: true, wantReason: ReasonLocalOwner,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			v := Authorize(tc.list, tc.actor, tc.op, tc.now)
			if v.Allowed != tc.wantOK || v.Reason != tc.wantReason || v.Level != tc.wantLevel {
				t.Fatalf("verdict = %+v, want allowed=%v reason=%s level=%v", v, tc.wantOK, tc.wantReason, tc.wantLevel)
			}
		})
	}
}

// TestAcceptanceScenarioManageLevel walks the acceptance scenario for the
// manage level: it can withdraw and tighten, but not let anything in or back.
func TestAcceptanceScenarioManageLevel(t *testing.T) {
	a := person(t, 1)
	list := mustList(t, Entry{Person: a, Level: LevelManage})
	actor := RemotePerson(a, validCert(LevelFullControl))
	tests := []struct {
		name string
		op   Operation
		want bool
	}{
		{"manage cannot install an adapter", Operation{Kind: OpInstallAdapter}, false},
		{"manage cannot add or loosen a preset", Operation{Kind: OpChangePreset, PresetChange: PresetChangeLoosening}, false},
		{"manage cannot change a preset it cannot classify", Operation{Kind: OpChangePreset}, false},
		{"manage cannot confirm trust establishment", Operation{Kind: OpConfirmTrust}, false},
		{"manage cannot enable inheritance", Operation{Kind: OpEnableInheritance}, false},
		{"manage cannot extend the grace period", Operation{Kind: OpExtendGracePeriod}, false},
		{"manage cannot clear a locally-revoked record", Operation{Kind: OpClearLocallyRevoked}, false},
		{"manage cannot loosen policy", Operation{Kind: OpChangePolicy, PolicyChange: authz.Loosen}, false},
		{"manage cannot change policy with the classification unset", Operation{Kind: OpChangePolicy}, false},
		{"manage cannot change policy with an out-of-range classification", Operation{Kind: OpChangePolicy, PolicyChange: 99}, false},
		{"manage can revoke trust", Operation{Kind: OpRevokeTrust}, true},
		{"manage can locally revoke a node", Operation{Kind: OpLocallyRevokeNode}, true},
		{"manage can reject a trust request", Operation{Kind: OpRejectTrustRequest}, true},
		{"manage can switch LAN discovery on or off", Operation{Kind: OpSetLANDiscovery}, true},
		{"manage can remove or narrow a preset", Operation{Kind: OpChangePreset, PresetChange: PresetChangeNarrowing}, true},
		{"manage can query the result of its own operation", Operation{Kind: OpQueryOperationResult}, true},
		{"manage can disable inheritance", Operation{Kind: OpDisableInheritance}, true},
		{"manage can shorten the grace period", Operation{Kind: OpShortenGracePeriod}, true},
		{"manage can tighten policy", Operation{Kind: OpChangePolicy, PolicyChange: authz.Tighten}, true},
		{"manage can apply a policy change that moves nothing", Operation{Kind: OpChangePolicy, PolicyChange: authz.Unchanged}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Authorize(list, actor, tc.op, t0).Allowed; got != tc.want {
				t.Fatalf("allowed = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestAcceptanceScenarioBatchRevocationIsPerNode covers a batch local
// revocation: the CLI sends OpLocallyRevokeNode to each node and every node
// answers from its own list, so one person can be allowed on one node and
// refused on another.
func TestAcceptanceScenarioBatchRevocationIsPerNode(t *testing.T) {
	a := person(t, 1)
	actor := RemotePerson(a, validCert(LevelFullControl))
	op := Operation{Kind: OpLocallyRevokeNode}
	managesHere := mustList(t, Entry{Person: a, Level: LevelManage})
	absentThere := mustList(t)

	if v := Authorize(managesHere, actor, op, t0); !v.Allowed {
		t.Fatalf("on the node whose list gives manage the revocation must be allowed, got %+v", v)
	}
	if v := Authorize(absentThere, actor, op, t0); v.Allowed || v.Reason != ReasonNotOnList {
		t.Fatalf("on the node whose list lacks the person the verdict = %+v, want denied as not on list", v)
	}
}

func TestFullControlMayChangePresets(t *testing.T) {
	a := person(t, 1)
	list := mustList(t, Entry{Person: a, Level: LevelFullControl})
	actor := RemotePerson(a, validCert(LevelFullControl))
	for _, class := range []PresetChangeClass{PresetChangeLoosening, PresetChangeNarrowing, PresetChangeUnclassified} {
		if !Authorize(list, actor, Operation{Kind: OpChangePreset, PresetChange: class}, t0).Allowed {
			t.Fatalf("full control must be allowed to change presets (class %d)", class)
		}
	}
}

func TestFullControlMayLoosenPolicy(t *testing.T) {
	a := person(t, 1)
	list := mustList(t, Entry{Person: a, Level: LevelFullControl})
	actor := RemotePerson(a, validCert(LevelFullControl))
	for _, class := range []authz.Change{authz.Loosen, authz.Tighten, authz.Unchanged, 0, 99} {
		if !Authorize(list, actor, Operation{Kind: OpChangePolicy, PolicyChange: class}, t0).Allowed {
			t.Fatalf("full control must be allowed to change policy (class %d)", class)
		}
	}
}

func TestRemovalAndDowngradeTakeEffectImmediately(t *testing.T) {
	a := person(t, 1)
	actor := RemotePerson(a, validCert(LevelFullControl))
	op := Operation{Kind: OpRevokeTrust}

	before := mustList(t, Entry{Person: a, Level: LevelManage})
	after := mustList(t)
	downgraded := mustList(t, Entry{Person: a, Level: LevelView})

	if !Authorize(before, actor, op, t0).Allowed {
		t.Fatal("setup: the listed manager must be allowed")
	}
	if v := Authorize(after, actor, op, t0); v.Allowed || v.Reason != ReasonNotOnList {
		t.Fatalf("after removal verdict = %+v, want denied as not on list", v)
	}
	if v := Authorize(downgraded, actor, op, t0); v.Allowed || v.Reason != ReasonListLevelTooLow {
		t.Fatalf("after downgrade verdict = %+v, want denied for list level", v)
	}
}

func TestEffectiveLevelStanding(t *testing.T) {
	a := person(t, 1)
	list := mustList(t, Entry{Person: a, Level: LevelFullControl})
	s := EffectiveLevel(list, a, validCert(LevelManage), t0)
	want := Standing{Level: LevelManage, ListLevel: LevelFullControl, CapLevel: LevelManage, Reason: ReasonOK}
	if s != want {
		t.Fatalf("standing = %+v, want %+v", s, want)
	}
}

// TestRemoteNeverReachesLocalOwnerOnly is an exhaustive property over every
// level combination, entry state and certificate state.
func TestRemoteNeverReachesLocalOwnerOnly(t *testing.T) {
	a := person(t, 1)
	op := Operation{Kind: OpModifyAuthorizedList}
	for listLevel := Level(-1); listLevel <= LevelFullControl+1; listLevel++ {
		for devCap := Level(-1); devCap <= LevelFullControl+1; devCap++ {
			cert := validCert(devCap)
			list := List{entries: []Entry{{Person: a, Level: listLevel}}} // deliberately bypasses validation
			if Authorize(list, RemotePerson(a, cert), op, t0).Allowed {
				t.Fatalf("remote person allowed to modify the list at list=%v cap=%v", listLevel, devCap)
			}
		}
	}
}

// TestHigherLevelNeverLosesRights is an exhaustive monotonicity property: for
// every operation, raising the effective level never turns an allow into a
// deny.
func TestHigherLevelNeverLosesRights(t *testing.T) {
	a := person(t, 1)
	for _, tc := range catalogue {
		prev := false
		for lvl := LevelNone; lvl <= LevelFullControl; lvl++ {
			list := List{entries: []Entry{{Person: a, Level: lvl}}}
			got := Authorize(list, RemotePerson(a, validCert(LevelFullControl)), tc.op, t0).Allowed
			if prev && !got {
				t.Fatalf("%s: allowed at a lower level but denied at %v", tc.name, lvl)
			}
			prev = got
		}
	}
}

// TestUnknownRequirementIsDeniedToRemotePersons covers a requirement value the
// code does not know, which can only appear if the catalogue and Requirement
// drift apart: it must fail closed, even for a full-control person.
func TestUnknownRequirementIsDeniedToRemotePersons(t *testing.T) {
	a := person(t, 1)
	list := mustList(t, Entry{Person: a, Level: LevelFullControl})
	actor := RemotePerson(a, validCert(LevelFullControl))
	for _, req := range []Requirement{0, RequireLocalOwner + 1, 99} {
		v := authorizeRemote(list, actor, req, t0)
		if v.Allowed || v.Reason != ReasonLocalOwnerOnly {
			t.Errorf("requirement %d: verdict = %+v, want denied as local owner only", req, v)
		}
	}
}
