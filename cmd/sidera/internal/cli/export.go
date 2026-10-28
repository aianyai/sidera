package cli

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"

	"github.com/aianyai/sidera/internal/node/apiwire"
	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/keys"
	"github.com/aianyai/sidera/internal/node/localhttp"
	"github.com/aianyai/sidera/internal/node/manageapi"
)

type revocationExport struct {
	Armored string `json:"armored"`
	Digest  string `json:"digest"`
}
type exportResponse struct {
	Result revocationExport `json:"result"`
}

func (c *command) exportRevocation(ctx context.Context, client *manageapi.Client) error {
	raw, err := client.Call(ctx, "identity.export_revocation", manageapi.Request{})
	if err != nil {
		if apiwire.FromError(err).Code != "daemon_not_running" {
			return err
		}
		reader, err := fsx.OpenOS(c.home, rand.Reader)
		if err != nil {
			return err
		}
		statement, err := keys.ReadSelfRevocation(reader)
		if err != nil {
			return err
		}
		armor, digest := keys.FormatRevocationExport(statement)
		raw, err = localhttp.EncodeRequest(exportResponse{Result: revocationExport{Armored: string(armor), Digest: digest.String()}})
		if err != nil {
			return err
		}
	}
	var response exportResponse
	if err := json.Unmarshal(raw, &response); err != nil || response.Result.Armored == "" || response.Result.Digest == "" {
		return apiwire.OutcomeUnknown("")
	}
	if c.json {
		_, err = c.Stdout.Write(raw)
	} else {
		_, err = fmt.Fprint(c.Stdout, response.Result.Armored)
	}
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(c.Stderr, "Keep this self-revocation outside this device, then run `sidera identity confirm-backup %s`.\n", response.Result.Digest)
	return err
}
