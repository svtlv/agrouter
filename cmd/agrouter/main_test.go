package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRun_Version(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"--version"}, strings.NewReader(""), &stdout, &stderr)

	assert.Equal(t, 0, code)
	assert.Empty(t, stderr.String())
	lines := strings.Split(strings.TrimRight(stdout.String(), "\n"), "\n")
	require.Len(t, lines, 1)
	assert.NotEmpty(t, strings.TrimSpace(lines[0]))
}

func TestRun_Help(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"--help"}, strings.NewReader(""), &stdout, &stderr)

	assert.Equal(t, 0, code)
	assert.Empty(t, stderr.String())
	assert.Contains(t, stdout.String(), "agrouter")
	assert.Contains(t, stdout.String(), "version") // "/version" on Windows, "--version" elsewhere
}

func TestRun_UnknownFlag(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"--no-such-flag"}, strings.NewReader(""), &stdout, &stderr)

	assert.Equal(t, 2, code)
	assert.Empty(t, stdout.String())
	assert.True(t, strings.HasPrefix(stderr.String(), "agrouter: "), stderr.String())
	assert.Equal(t, 1, strings.Count(stderr.String(), "\n"))
}

func TestVersion_NotEmpty(t *testing.T) {
	assert.NotEmpty(t, version())
}

func TestVersion_StampedRevision(t *testing.T) {
	old := revision
	t.Cleanup(func() { revision = old })

	revision = "v9.8.7"
	assert.Equal(t, "v9.8.7", version())
}
