package command

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/ferro-labs/ai-gateway/cli/internal/api"
	"github.com/ferro-labs/ai-gateway/cli/internal/table"
)

func newKeysCmd() *cobra.Command {
	keys := &cobra.Command{
		Use:   "keys",
		Short: "Manage gateway API keys",
		Args:  cobra.NoArgs,
		RunE:  func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	keys.AddCommand(newKeysListCmd(), newKeysGetCmd(), newKeysCreateCmd(),
		newKeysRotateCmd(), newKeysRevokeCmd())
	return keys
}

func newKeysListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   verbList,
		Short: "List API keys with their derived state",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			d := deps(cmd)
			rows, err := d.Client.Keys(cmd.Context())
			if err != nil {
				return err
			}
			if d.Printer.Format != FormatTable {
				return d.printStructured(rows)
			}
			cells := make([][]string, 0, len(rows))
			for _, k := range rows {
				cells = append(cells, []string{
					// ID leads because rotate and revoke take one, and the
					// listing is the only place to find it.
					k.ID,
					k.Name,
					// Already masked on the wire; ferro never unmasks.
					k.Key,
					strings.Join(k.Scopes, ","),
					table.FmtTimePtr(k.ExpiresAt),
					table.FmtTimePtr(k.LastUsedAt),
					fmt.Sprintf("%d", k.UsageCount),
					api.KeyState(k),
				})
			}
			d.Printer.Table([]string{"ID", colName, "KEY", colScopes, colExpires, "LAST USED", "USES", colState}, cells)
			return nil
		},
	}
}

func newKeysGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "get <id>",
		Short: "Show one key",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			d := deps(cmd)
			k, err := d.Client.Key(cmd.Context(), args[0])
			if err != nil {
				// A gateway build without GET /admin/keys/{id} still serves the
				// row in the list, so fall back rather than fail.
				if !api.IsNotSupported(err) {
					return err
				}
				if k, err = findKey(cmd, args[0]); err != nil {
					return err
				}
			}
			return d.printStructured(k)
		},
	}
}

// findKey resolves one key out of the full listing.
func findKey(cmd *cobra.Command, id string) (*api.Key, error) {
	rows, err := deps(cmd).Client.Keys(cmd.Context())
	if err != nil {
		return nil, err
	}
	for i := range rows {
		if rows[i].ID == id {
			return &rows[i], nil
		}
	}
	return nil, fmt.Errorf("no key with id %q on this gateway", id)
}

func newKeysCreateCmd() *cobra.Command {
	create := &cobra.Command{
		Use:   "create",
		Short: "Create an API key (the secret is shown exactly once)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			d := deps(cmd)
			name, _ := cmd.Flags().GetString("name")
			scope, _ := cmd.Flags().GetString("scope")
			expiresIn, _ := cmd.Flags().GetDuration("expires-in")

			req, err := keyCreateRequest(name, scope, expiresIn)
			if err != nil {
				return err
			}
			k, err := d.Client.CreateKey(cmd.Context(), req)
			if err != nil {
				return err
			}
			return printSecretOnce(d, k, "secret shown once — the gateway stores only a hash")
		},
	}
	create.Flags().String("name", "", "key name (required)")
	create.Flags().String("scope", api.ScopeReadOnly,
		api.ScopeAdmin+"|"+api.ScopeReadOnly+" (always sent explicitly)")
	create.Flags().Duration("expires-in", 720*time.Hour, "lifetime from now; 0 for no expiry")
	return create
}

// keyCreateRequest validates client-side so a bad flag costs no request.
func keyCreateRequest(name, scope string, expiresIn time.Duration) (api.KeyCreateRequest, error) {
	// These messages lead with a word rather than a flag: the styled error
	// renderer capitalises the first character, which mangles "--name".
	if name == "" {
		return api.KeyCreateRequest{}, errors.New(
			"a key name is required, so pass --name (a log row names the key, and an unnamed one is unattributable once it is revoked)")
	}
	// The two scope names come from internal/api, which derives the same
	// vocabulary from the wire; a second copy here could drift from it.
	if scope != api.ScopeAdmin && scope != api.ScopeReadOnly {
		return api.KeyCreateRequest{}, fmt.Errorf(
			"scope %q is not valid: --scope must be %s|%s — ferro always sends one explicitly rather than relying on the gateway's default",
			scope, api.ScopeAdmin, api.ScopeReadOnly)
	}
	if expiresIn < 0 {
		return api.KeyCreateRequest{}, errors.New("expiry must be 0 for no expiry or a positive duration")
	}
	req := api.KeyCreateRequest{Name: name, Scopes: []string{scope}}
	if expiresIn > 0 {
		t := time.Now().Add(expiresIn).UTC()
		req.ExpiresAt = &t
	}
	return req, nil
}

func newKeysRotateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "rotate <id>",
		Short: "Rotate a key (the previous secret dies immediately)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			d := deps(cmd)
			k, err := d.Client.RotateKey(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			return printSecretOnce(d, k,
				"previous secret stopped authenticating; the new one is shown once")
		},
	}
}

// printSecretOnce is the one deliberate credential display: the secret goes to
// stdout so it can be piped into a secret store, and every word about it goes
// to stderr so the pipe stays clean.
func printSecretOnce(d *runtimeDeps, k *api.Key, warning string) error {
	d.Printer.Warn("%s", warning)
	if d.Printer.Format != FormatTable {
		return d.printStructured(k)
	}
	_, err := fmt.Fprintln(d.Printer.Out, k.Key)
	return err
}

func newKeysRevokeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "revoke <id>",
		Short: "Revoke a key (immediate and irreversible)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			d := deps(cmd)
			if err := d.Client.RevokeKey(cmd.Context(), args[0]); err != nil {
				return err
			}
			d.Printer.OK("revoked %s", args[0])
			return nil
		},
	}
}
