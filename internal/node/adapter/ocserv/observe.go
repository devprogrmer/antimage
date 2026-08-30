package ocserv

import (
	"bufio"
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/amyrm/antimage/internal/node/adapter"
)

// Observe reads host truth. It never mutates anything.
//
// The two files are read differently on purpose.
//
// ocserv.conf carries a marker with the checksum of the body the adapter
// wrote. Recomputing that checksum from what is on disk now and comparing it
// to the marker is what catches a hand edit: the marker still claims the old
// content, the file no longer matches it, and Plan reports drift rather than
// silently overwriting somebody's change.
//
// The passwd file cannot work that way. ocpasswd writes salted crypt hashes,
// so the same users and passwords produce different bytes every time -- a
// byte checksum would report drift on every pass and rewrite the file forever.
// What IS stable is the set of account names, so that is what is compared. A
// hand-added account is detected; a hand-changed password for an account that
// should exist is not, and that is the honest limit of this approach.
func (a *Adapter) Observe(ctx context.Context) (adapter.Observed, error) {
	var obs adapter.Observed

	confPath := filepath.Join(a.dir, confName)
	body, err := os.ReadFile(confPath)
	if err != nil {
		// No config file: nothing of ours is on this host. Not an error --
		// this is the ordinary state of a node before its first apply.
		return obs, nil
	}

	content := string(body)
	newline := strings.IndexByte(content, '\n')
	if newline < 0 {
		// A file with no line break cannot carry our marker, so it is not ours.
		obs.Services = append(obs.Services, adapter.ObservedService{Present: true})
		return obs, nil
	}

	serviceID, recorded, ok := parseMarker(content[:newline])
	if !ok {
		// Present, and somebody else's. Reported rather than overwritten.
		obs.Services = append(obs.Services, adapter.ObservedService{Present: true})
		return obs, nil
	}

	// Checksum of what is actually there now, not of what the marker claims.
	// Comparing the two is the whole point.
	actual := checksumOf(content[newline+1:])

	names, err := a.readUsernames()
	if err != nil {
		return adapter.Observed{}, err
	}

	obs.Services = append(obs.Services, adapter.ObservedService{
		ID:      serviceID,
		Present: true,
		// Managed AND unedited. A file whose body no longer matches its own
		// marker is still ours, but reporting it as managed would let Plan
		// treat a hand edit as converged.
		Managed:  actual == recorded,
		Checksum: serviceChecksum(actual, checksumOf(strings.Join(names, "\n"))),
	})
	return obs, nil
}

// readUsernames returns the sorted account names in the passwd file.
//
// Format is "username:group:hash". A malformed line is skipped rather than
// failing the read: the file is shared with ocpasswd, and one unparseable line
// should not stop the adapter observing the rest of the host.
func (a *Adapter) readUsernames() ([]string, error) {
	f, err := os.Open(filepath.Join(a.dir, passwdName))
	if err != nil {
		if os.IsNotExist(err) {
			// No passwd file is a legitimate state: a service with no users
			// yet. Distinct from an unreadable one, which is a real failure.
			return nil, nil
		}
		return nil, err
	}
	defer func() { _ = f.Close() }()

	var names []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, _, found := strings.Cut(line, ":")
		if !found || name == "" {
			continue
		}
		names = append(names, name)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	sort.Strings(names)
	return names, nil
}
