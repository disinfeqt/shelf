package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAllFeedDefaultsAndPersistence(t *testing.T) {
	for _, tc := range []struct {
		name, content string
		want          AllFeed
	}{
		{"legacy config", `{"roots":[],"ignore":[]}`, AllFeed{Photos: true, Videos: true}},
		{"one preference", `{"all_feed":{"photos":false}}`, AllFeed{Videos: true}},
		{"both hidden", `{"all_feed":{"photos":false,"videos":false}}`, AllFeed{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Cleanup(SwapForTest(Default()))
			path := filepath.Join(t.TempDir(), "config.json")
			require.NoError(t, os.WriteFile(path, []byte(tc.content), 0o600))
			require.NoError(t, Load(path))
			assert.Equal(t, tc.want, Current().AllFeed)
			require.NoError(t, Load(path))
			assert.Equal(t, tc.want, Current().AllFeed)
		})
	}
}
