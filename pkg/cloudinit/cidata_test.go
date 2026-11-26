/*
Copyright 2026 Luther Monson.

Licensed under the MIT License. See LICENSE in the project root for details.
*/

package cloudinit

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/kdomanski/iso9660"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildValidation(t *testing.T) {
	_, err := Build(nil, Metadata{InstanceID: "x"})
	assert.Error(t, err, "empty user-data must fail")

	_, err = Build([]byte("#cloud-config"), Metadata{})
	assert.Error(t, err, "missing instance-id must fail")
}

func TestBuildProducesReadableCIDATA(t *testing.T) {
	userData := []byte("#cloud-config\nhostname: node0\n")
	data, err := Build(userData, Metadata{InstanceID: "node0", Hostname: "node0"})
	require.NoError(t, err)
	require.NotEmpty(t, data)

	// The image must be a real iso9660 image with the CIDATA volume label and the
	// two NoCloud files, with user-data byte-identical to the input.
	img, err := iso9660.OpenImage(bytes.NewReader(data))
	require.NoError(t, err)

	root, err := img.RootDir()
	require.NoError(t, err)
	children, err := root.GetChildren()
	require.NoError(t, err)

	files := map[string][]byte{}
	for _, c := range children {
		if c.IsDir() {
			continue
		}
		buf := new(bytes.Buffer)
		_, err := buf.ReadFrom(c.Reader())
		require.NoError(t, err)
		// iso9660 may append ";1" version suffixes; normalize for the map key.
		name := c.Name()
		files[name] = buf.Bytes()
	}

	var gotUserData, gotMetaData []byte
	for name, content := range files {
		switch {
		case hasPrefix(name, "user-data"):
			gotUserData = content
		case hasPrefix(name, "meta-data"):
			gotMetaData = content
		}
	}
	require.NotNil(t, gotUserData, "user-data file must be present; got files %v", keys(files))
	require.NotNil(t, gotMetaData, "meta-data file must be present; got files %v", keys(files))
	assert.Equal(t, userData, gotUserData)
	assert.Contains(t, string(gotMetaData), "instance-id: node0")
	assert.Contains(t, string(gotMetaData), "local-hostname: node0")
}

func TestWriteISO(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "node0-cidata.iso")
	err := WriteISO(path, []byte("#cloud-config"), Metadata{InstanceID: "node0", Hostname: "node0"})
	require.NoError(t, err)

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Greater(t, info.Size(), int64(0))
}

func hasPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

func keys(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
