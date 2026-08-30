package openvpn

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/amyrm/antimage/internal/node/adapter"
)

// Observe reads host truth. It never mutates anything.
//
// All three files are checksummed the same way, and exactly: the adapter
// writes every byte of each, so unlike ocserv there is no third-party tool
// introducing a random salt. A file whose body no longer matches the checksum
// in its own marker was edited by hand, and Plan reports that rather than
// overwriting it.
func (a *Adapter) Observe(ctx context.Context) (adapter.Observed, error) {
	var obs adapter.Observed

	confID, confSum, confState := a.inspect(confName)
	if confState == absent {
		// No server config: nothing of ours is here. The ordinary state of a
		// node before its first apply.
		return obs, nil
	}
	if confState == foreign {
		// Present and somebody else's. Reported, never touched.
		obs.Services = append(obs.Services, adapter.ObservedService{Present: true})
		return obs, nil
	}

	_, verifySum, verifyState := a.inspect(verifyName)
	_, usersSum, usersState := a.inspect(usersName)

	// Managed means all three are ours AND none has been edited. A partial
	// answer here would let Plan treat a hand-edited verify script as
	// converged -- and that script is what decides who may log in.
	managed := confState == managed_ &&
		verifyState == managed_ && usersState == managed_

	obs.Services = append(obs.Services, adapter.ObservedService{
		ID:       confID,
		Present:  true,
		Managed:  managed,
		Checksum: serviceChecksum(confSum, verifySum, usersSum),
	})
	return obs, nil
}

type fileState int

const (
	absent fileState = iota
	// foreign: present with no marker of ours, or edited since we wrote it.
	foreign
	managed_
)

// inspect reads one file and reports whether it is ours and unmodified.
//
// The checksum returned is of the CONTENT AS IT IS NOW, not the one recorded
// in the marker. Comparing the two is what detects a hand edit; returning the
// recorded value would make every file look untouched forever.
func (a *Adapter) inspect(name string) (serviceID int64, checksum string, state fileState) {
	body, err := os.ReadFile(filepath.Join(a.dir, name))
	if err != nil {
		return 0, "", absent
	}
	content := string(body)
	nl := strings.IndexByte(content, '\n')
	if nl < 0 {
		return 0, "", foreign
	}
	id, recorded, ok := parseMarker(content[:nl])
	if !ok {
		return 0, "", foreign
	}
	actual := checksumOf(content[nl+1:])
	if actual != recorded {
		// Ours, but edited. Reported with the actual checksum so Plan sees a
		// difference, and as not-managed so Plan refuses to overwrite it.
		return id, actual, foreign
	}
	return id, actual, managed_
}
