package main

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ggoosen/cairn/internal/config"
	"github.com/ggoosen/cairn/internal/event"
	"github.com/ggoosen/cairn/internal/identity"
)

func newIdentityCmd(dirFlag *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "identity",
		Short: "Inspect this device's cairn identity",
	}
	cmd.AddCommand(newIdentityShowCmd(dirFlag))
	cmd.AddCommand(newIdentityExportRootCmd(dirFlag))
	groupGuard(cmd)
	return cmd
}

// newIdentityExportRootCmd implements `cairn identity export-root`
// (FIX-F5, RULINGS.md §root-key): export the root key material for OFFLINE
// operator storage; optionally (prompted, never forced in P0) remove the
// device-local copy after a verified export.
func newIdentityExportRootCmd(dirFlag *string) *cobra.Command {
	var out string
	var removeLocal bool
	cmd := &cobra.Command{
		Use:   "export-root",
		Short: "Export the mesh root key for offline operator storage (optionally removing the device-local copy)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			dir, err := config.PortableDir(*dirFlag)
			if err != nil {
				return err
			}
			loaded, err := identity.Load(dir)
			if err != nil {
				return err
			}
			if out == "" {
				out = "cairn-root-" + loaded.Portable.CairnID[:8] + ".key"
			}
			absOut, err := filepath.Abs(out)
			if err != nil {
				return err
			}
			absDir, err := filepath.Abs(dir)
			if err != nil {
				return err
			}
			if absOut == absDir || strings.HasPrefix(absOut, absDir+string(filepath.Separator)) {
				return fmt.Errorf("refusing to export the root key INSIDE the portable mesh directory (%s) — it would travel with backups; choose an offline destination", dir)
			}

			src := filepath.Join(loaded.DeviceDir, config.RootKeyName)
			blob, err := os.ReadFile(src)
			if err != nil {
				return fmt.Errorf("root key unavailable (already exported and removed?): %w", err)
			}
			if _, err := os.Stat(absOut); err == nil {
				return fmt.Errorf("%s already exists — refusing to overwrite key material", absOut)
			}
			if err := os.WriteFile(absOut, blob, 0o600); err != nil {
				return err
			}
			// VERIFIED export: read back and compare before offering removal
			back, err := os.ReadFile(absOut)
			if err != nil || !bytes.Equal(back, blob) {
				return fmt.Errorf("export verification failed: %v", err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "root key exported to %s (verified)\n", absOut)
			fmt.Fprintln(cmd.OutOrStdout(), "Store it OFFLINE (password manager, printed copy, or air-gapped drive).")
			fmt.Fprintln(cmd.OutOrStdout(), "It is required for: device migration (device.add/revoke signing) and P1 device enrolment.")

			remove := removeLocal
			if !remove {
				if fi, _ := os.Stdin.Stat(); fi != nil && (fi.Mode()&os.ModeCharDevice) != 0 {
					fmt.Fprint(cmd.OutOrStdout(), "Remove the device-local copy now? Migration will then REQUIRE this export. [y/N]: ")
					var answer string
					fmt.Fscanln(cmd.InOrStdin(), &answer)
					remove = strings.EqualFold(strings.TrimSpace(answer), "y")
				} else {
					fmt.Fprintln(cmd.OutOrStdout(), "Device-local copy kept (non-interactive; pass --remove-local to remove it after export).")
				}
			}
			if remove {
				if err := os.Remove(src); err != nil {
					return err
				}
				fmt.Fprintln(cmd.OutOrStdout(), "Device-local root key removed. `cairn migrate` now requires restoring the exported key to:")
				fmt.Fprintln(cmd.OutOrStdout(), "  "+src)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&out, "out", "", "export destination (default ./cairn-root-<id>.key; must be OUTSIDE the mesh dir)")
	cmd.Flags().BoolVar(&removeLocal, "remove-local", false, "remove the device-local copy after a verified export (non-interactive)")
	return cmd
}

func newIdentityShowCmd(dirFlag *string) *cobra.Command {
	return &cobra.Command{
		Use:   "show",
		Short: "Show cairn, device, and key identity; verifies the genesis event",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := cmd.OutOrStdout()
			dir, err := config.PortableDir(*dirFlag)
			if err != nil {
				return err
			}
			loaded, err := identity.Load(dir)
			if err != nil {
				return err
			}
			// Every start against an initialized cairn re-checks encryption;
			// the persisted override warns each time (rulings §9).
			if err := loaded.StartupCheck(nil, cmd.ErrOrStderr()); err != nil {
				return err
			}

			env, pl, err := loaded.GenesisRecord()
			if err != nil {
				return err
			}
			devicePub, err := pl.InitialDeviceCert.DevicePublicKey()
			if err != nil {
				return err
			}
			rootPub, err := base64.StdEncoding.DecodeString(pl.RootPubkey)
			if err != nil {
				return err
			}

			fmt.Fprintf(out, "cairn_id:        %s\n", loaded.Portable.CairnID)
			fmt.Fprintf(out, "portable dir:    %s\n", loaded.Dir)
			fmt.Fprintf(out, "device state:    %s\n", loaded.DeviceDir)
			fmt.Fprintf(out, "device_id:       %s (generation %d)\n", loaded.Device.DeviceID, loaded.Device.OriginGeneration)
			if pl.InitialDeviceCert.DisplayName != "" {
				fmt.Fprintf(out, "display name:    %s\n", pl.InitialDeviceCert.DisplayName)
			}
			fmt.Fprintf(out, "device key id:   %s\n", event.KeyID(devicePub))
			fmt.Fprintf(out, "root key id:     %s\n", event.KeyID(rootPub))
			fmt.Fprintf(out, "genesis event:   %s (verified: envelope signature, event_id, root-signed cert)\n", env.EventID)
			if loaded.Device.AllowUnencrypted {
				fmt.Fprintf(out, "encryption:      OVERRIDDEN (--allow-unencrypted persisted device-local)\n")
			}
			return nil
		},
	}
}
