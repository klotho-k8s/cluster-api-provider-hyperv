/*
Copyright 2026 Luther Monson.

Licensed under the MIT License. See LICENSE in the project root for details.
*/

// Package cloudinit builds a NoCloud "cidata" ISO that delivers bootstrap data
// to a cloud-init enabled guest. The ISO is an iso9660 image with the volume
// label CIDATA containing user-data and meta-data files, which cloud-init's
// NoCloud datasource reads when the ISO is attached as a DVD.
package cloudinit

import (
	"bytes"
	"fmt"
	"os"

	"github.com/kdomanski/iso9660"
)

// VolumeLabel is the volume identifier cloud-init's NoCloud datasource looks
// for. It must be exactly "CIDATA".
const VolumeLabel = "CIDATA"

// Metadata is the minimal cloud-init meta-data document. instance-id must be
// stable for a given machine so cloud-init does not re-run first-boot modules.
type Metadata struct {
	// InstanceID uniquely and stably identifies the instance.
	InstanceID string
	// Hostname is the local hostname set in the guest.
	Hostname string
}

// renderMetadata produces the YAML meta-data document.
func (m Metadata) render() []byte {
	return []byte(fmt.Sprintf("instance-id: %s\nlocal-hostname: %s\n", m.InstanceID, m.Hostname))
}

// Build returns the bytes of a NoCloud cidata ISO carrying the given user-data
// (the machine's bootstrap secret, verbatim) and meta-data. The caller writes
// the result to a path on the Hyper-V host and attaches it to a DVD drive.
func Build(userData []byte, meta Metadata) ([]byte, error) {
	if len(userData) == 0 {
		return nil, fmt.Errorf("cloudinit: user-data must not be empty")
	}
	if meta.InstanceID == "" {
		return nil, fmt.Errorf("cloudinit: meta-data instance-id must not be empty")
	}

	writer, err := iso9660.NewWriter()
	if err != nil {
		return nil, fmt.Errorf("cloudinit: creating iso writer: %w", err)
	}
	defer func() { _ = writer.Cleanup() }()

	if err := writer.AddFile(bytes.NewReader(userData), "user-data"); err != nil {
		return nil, fmt.Errorf("cloudinit: adding user-data: %w", err)
	}
	if err := writer.AddFile(bytes.NewReader(meta.render()), "meta-data"); err != nil {
		return nil, fmt.Errorf("cloudinit: adding meta-data: %w", err)
	}

	var buf bytes.Buffer
	if err := writer.WriteTo(&buf, VolumeLabel); err != nil {
		return nil, fmt.Errorf("cloudinit: writing iso: %w", err)
	}
	return buf.Bytes(), nil
}

// WriteISO builds the cidata ISO and writes it to path. It is a convenience for
// the controller, which needs the ISO on the host filesystem so Hyper-V can
// attach it.
func WriteISO(path string, userData []byte, meta Metadata) error {
	data, err := Build(userData, meta)
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("cloudinit: writing iso to %s: %w", path, err)
	}
	return nil
}
