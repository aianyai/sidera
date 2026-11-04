package adapter

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/aianyai/sidera/cmd/sidera-adapter-hermes/internal/config"
	"github.com/aianyai/sidera/internal/release"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
	"github.com/aianyai/sidera/sdk/go/manifest"
)

// Manifest is the manifest of the package for one platform. The adapter declares
// only direct endpoints: it starts no runtime and has no presets, since it creates
// and drives no Hermes process. It requires the hermes program, which the probe
// then checks in depth.
func Manifest(goos, goarch string) (manifest.Manifest, error) {
	m := manifest.Manifest{
		ManifestVersion: manifest.Version,
		ID:              AdapterID,
		Version:         release.Version(),
		Protocol:        adapterproto.SupportedRange(),
		// manifest.WritePackage adds .exe to the program of a Windows package.
		Entry:     manifest.Entry{Program: "bin/" + ProgramName, Args: []string{"serve"}},
		Platforms: []manifest.Platform{{OS: goos, Arch: goarch}},
		Requires:  []manifest.Requirement{{Program: "hermes"}},
		Config:    &manifest.Config{Instance: settings()},
	}
	return m, m.Validate()
}

// settings declares the instance configuration that config.Decode reads: the same
// keys, the same defaults. A key declared here and unknown to Decode would be
// refused at start; a default that differs would show the owner one value and run
// another.
func settings() []manifest.Field {
	str := func(key, title, desc string) manifest.Field {
		return manifest.Field{Key: key, Type: manifest.FieldString, Title: title, Description: desc}
	}
	required := func(f manifest.Field) manifest.Field { f.Required = true; return f }
	withDefault := func(f manifest.Field, def any) manifest.Field {
		raw, err := json.Marshal(def)
		if err != nil {
			panic(err)
		}
		f.Default = raw
		return f
	}
	duration := func(key, title, desc string, def time.Duration) manifest.Field {
		return withDefault(str(key, title, desc+" A duration such as 90m or 168h."), durationText(def))
	}
	integer := func(key, title, desc string, def, min, max int) manifest.Field {
		lo, hi := int64(min), int64(max)
		return withDefault(manifest.Field{Key: key, Type: manifest.FieldInteger, Title: title, Description: desc, Min: &lo, Max: &hi}, def)
	}
	dir := func(key, title, desc string) manifest.Field {
		return manifest.Field{Key: key, Type: manifest.FieldDirectory, Title: title, Description: desc}
	}
	list := func(key, title, desc string) manifest.Field {
		return manifest.Field{Key: key, Type: manifest.FieldStringList, Title: title, Description: desc}
	}
	return []manifest.Field{
		withDefault(str(config.KeyHermesPath, "Hermes program", "The hermes executable, looked up on the path when it is a bare name. On Windows it must be hermes.exe, not a batch file."), config.DefaultHermesPath),
		required(dir(config.KeyHermesHome, "Hermes home", "HERMES_HOME given to every Hermes process the adapter starts.")),
		dir(config.KeyKanbanHome, "Kanban home", "HERMES_KANBAN_HOME, if you set one for Hermes."),
		required(str(config.KeyBoard, "Board", "The slug of the one Kanban board this instance serves.")),
		str(config.KeyDisplayName, "Name in the directory", "What other nodes see for this Hermes. Default: Hermes (<host name>)."),
		required(str(config.KeyHomeProfile, "Home profile", "The Hermes profile whose gateway is woken when a card has no conversation.")),
		required(str(config.KeyHomePlatform, "Home platform", "The messaging platform of the home conversation, for example feishu or telegram.")),
		required(str(config.KeyHomeChatID, "Home chat id", "The chat id of the home conversation.")),
		{Key: config.KeyHomeChatType, Type: manifest.FieldEnum, Title: "Home chat type", Options: config.ChatTypes()},
		str(config.KeyHomeThreadID, "Home thread id", ""),
		str(config.KeyHomeUserID, "Home user id", ""),
		str(config.KeyHomeUserIDAlt, "Home user id (alternate)", ""),
		str(config.KeyHomeParentChatID, "Home parent chat id", ""),
		str(config.KeyHomeGuildID, "Home guild id", ""),
		required(list(config.KeyAliases, "Aliases", "The only names a card may address, one per entry: <name>=<address>;modes=new_session,message;presets=<preset>[,<preset>]. Presets are required with new_session and refused without it.")),
		list(config.KeyCreatorProfiles, "Card creator profiles", "If set, only cards created by these Hermes profiles are served."),
		duration(config.KeyNoReplyTimeout, "No-reply timeout", "How long a card waits for the other side before it is blocked once.", config.DefaultNoReplyTimeout),
		duration(config.KeyClaimTTL, "Claim lifetime", "How long a claim on a card lasts; it must be longer than the no-reply timeout.", config.DefaultClaimTTL),
		duration(config.KeySessionRetention, "Session retention", "How long a conversation's remote session is kept after its last card.", config.DefaultSessionRetention),
		duration(config.KeyCloseWindow, "Close window", "How long the remote session of a cron or unattributed card is kept after the card finished.", config.DefaultCloseWindow),
		duration(config.KeyEndpointIdle, "Endpoint idle time", "How long a conversation's endpoint may go unused before it is retired.", config.DefaultEndpointIdle),
		duration(config.KeyPollInterval, "Board poll interval", "The pause between looks at the board.", config.DefaultPollInterval),
		integer(config.KeyMaxEndpoints, "Endpoints", "The most endpoints held, the instance endpoint and the unattributed one included.", config.DefaultMaxEndpoints, config.MinMaxEndpoints, config.MaxMaxEndpoints),
		integer(config.KeyMaxOpenSessions, "Open remote sessions", "The most remote sessions kept open.", config.DefaultMaxOpenSessions, 1, 1000),
	}
}

// durationText writes a duration in the form an owner writes one: whole hours,
// minutes or seconds.
func durationText(d time.Duration) string {
	switch {
	case d%time.Hour == 0:
		return fmt.Sprintf("%dh", d/time.Hour)
	case d%time.Minute == 0:
		return fmt.Sprintf("%dm", d/time.Minute)
	}
	return fmt.Sprintf("%ds", d/time.Second)
}
