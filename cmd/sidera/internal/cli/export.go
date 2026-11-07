package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/aianyai/sidera/internal/node/apiwire"
	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/keys"
	"github.com/aianyai/sidera/internal/node/manageapi"
)

type exportResponse struct {
	Result manageapi.RevocationExportResult `json:"result"`
}

func (c *command) exportRevocation(ctx context.Context, client *manageapi.Client, output *string) (err error) {
	raw, err := client.Call(ctx, "identity.export_revocation", manageapi.Request{})
	if err != nil {
		if apiwire.FromError(err).Code != "daemon_not_running" {
			return err
		}
		home, err := c.dataDir()
		if err != nil {
			return err
		}
		reader, err := fsx.OpenOS(home, c.random)
		if err != nil {
			return err
		}
		statement, err := keys.ReadSelfRevocation(reader)
		if err != nil {
			return err
		}
		armor, digest := keys.FormatRevocationExport(statement)
		var document bytes.Buffer
		encoder := json.NewEncoder(&document)
		encoder.SetEscapeHTML(false)
		if err := encoder.Encode(exportResponse{Result: manageapi.RevocationExportResult{Armored: string(armor), Digest: digest.String()}}); err != nil {
			return err
		}
		raw = document.Bytes()
	}
	answer, err := manageapi.ReadAnswer(raw)
	if err != nil {
		return err
	}
	var exported manageapi.RevocationExportResult
	if err := answer.DecodeResult(&exported); err != nil {
		return err
	}
	if exported.Armored == "" || exported.Digest == "" {
		return answer.Unreadable()
	}
	statement, err := keys.ParseRevocationExport([]byte(exported.Armored))
	if err != nil {
		return answer.Unreadable()
	}
	// The node answered and the answer reads, but the statement is not the one the
	// digest names: that is not an older program, and a copy of it must not be kept.
	if statement.Digest().String() != exported.Digest {
		return &failure{"the statement the node exported does not match its digest, so it was not written; run the command again"}
	}
	if output != nil {
		file, openErr := fsx.CreatePrivateFile(*output)
		if openErr != nil {
			return exportFileFailure(openErr)
		}
		// This defer is registered only after our exclusive create succeeded.
		// It never removes a file that already existed, including on output errors.
		defer func() {
			if err != nil {
				if removeErr := os.Remove(*output); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
					err = errors.Join(err, removeErr)
				}
				err = exportFileFailure(err)
			}
		}()
		_, writeErr := file.Write(statement.Wire())
		if writeErr == nil {
			writeErr = file.Sync()
		}
		closeErr := file.Close()
		if writeErr != nil || closeErr != nil {
			return errors.Join(writeErr, closeErr)
		}
	}
	if _, err = io.WriteString(c.errOut, renderExportReminder(c.errOut, exported.Digest)); err != nil {
		return err
	}
	if c.json && output != nil {
		var result struct {
			Result struct {
				Digest string `json:"digest"`
				File   string `json:"file"`
			} `json:"result"`
		}
		result.Result.Digest = exported.Digest
		result.Result.File = *output
		err = c.encode(result)
	} else if c.json {
		_, err = c.Stdout.Write(raw)
	} else if output != nil {
		_, err = fmt.Fprintln(c.Stdout, exported.Digest)
	} else {
		_, err = fmt.Fprint(c.Stdout, exported.Armored)
	}
	return err
}

// Only local file and stream errors enter here. Their path and OS failure
// explain what the owner can fix, including private-volume refusal; no response
// content, token or statement is included. Keep joined cleanup failures visible.
func exportFileFailure(err error) error {
	return apiwire.New(apiwire.CategoryInternal, apiwire.CodeInternal, err.Error())
}
