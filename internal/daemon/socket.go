package daemon

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
)

const socketFileName = "daemon.sock"

// maxSocketPathLen is the longest socket path we use directly. macOS caps
// sun_path at 104 bytes (Linux at 108); staying under 96 leaves headroom.
const maxSocketPathLen = 96

// SocketPath returns the unix socket path for a vault's dataDir
// (<vault>/.marmot-data). The primary path is <dataDir>/daemon.sock —
// hermetic for e2e (vault-derived) and skipped by all vault walkers. If the
// absolute path exceeds maxSocketPathLen (deep temp dirs would overflow
// sun_path), it falls back to os.TempDir()/marmot-<sha256(absVault)[:16]>.sock.
// Hash-keying by vault path keeps hermetic tests from colliding with real
// vaults. The chosen path is always published in daemon.info.json, so
// consumers never re-derive it and the fallback is invisible to them.
func SocketPath(dataDir string) string {
	abs, err := filepath.Abs(dataDir)
	if err != nil {
		abs = dataDir
	}
	primary := filepath.Join(abs, socketFileName)
	if len(primary) <= maxSocketPathLen {
		return primary
	}
	vault := filepath.Dir(abs)
	sum := sha256.Sum256([]byte(vault))
	return filepath.Join(os.TempDir(), "marmot-"+hex.EncodeToString(sum[:8])+".sock")
}
