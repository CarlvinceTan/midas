package server

import (
	"cmp"
	"crypto/rand"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"
)

// Team is a named group of agents and the people who work with them. Groups
// belong to a team, which is how a client shows "this conversation is part of
// this team" the way Polymux Teams does.
type Team struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Members []string `json:"members,omitempty"`
	At      int64    `json:"at,omitempty"`
}

// Host is a device the environment may reach. Pairing is local: the environment
// mints a short-lived code and accepts the device that presents it, so a device
// never arrives without the operator starting the handshake.
type Host struct {
	Name     string `json:"name"`
	Local    bool   `json:"local,omitempty"`
	Default  bool   `json:"default,omitempty"`
	At       int64  `json:"at,omitempty"`
	PairedAt int64  `json:"pairedAt,omitempty"`
}

// pairCode is one in-flight pairing handshake.
type pairCode struct {
	code      string
	name      string
	expiresAt time.Time
}

// teamsState is the environment's teams, groups, hosts and pairing state. It is
// guarded in one place so a change and its event cannot disagree.
type teamsState struct {
	mu     sync.Mutex
	teams  map[string]Team
	groups map[string]Group
	hosts  map[string]Host
	pairs  map[string]pairCode
	now    func() time.Time
}

func newTeamsState(hosts []HostConfig, groups map[string]Group) *teamsState {
	state := &teamsState{teams: map[string]Team{}, groups: map[string]Group{}, hosts: map[string]Host{}, pairs: map[string]pairCode{}, now: time.Now}
	if groups != nil {
		state.groups = groups
	}
	for _, host := range hosts {
		state.hosts[host.Name] = Host{Name: host.Name, Local: host.Local, Default: host.Default}
	}
	// The machine the environment runs on is always a host.
	if _, ok := state.hosts["local"]; !ok {
		state.hosts["local"] = Host{Name: "local", Local: true}
	}
	return state
}

// Teams lists every team, ordered by id.
func (s *teamsState) Teams() []Team {
	s.mu.Lock()
	defer s.mu.Unlock()
	teams := make([]Team, 0, len(s.teams))
	for _, team := range s.teams {
		teams = append(teams, team)
	}
	slices.SortStableFunc(teams, func(a, b Team) int { return cmp.Compare(a.ID, b.ID) })
	return teams
}

// PutTeam creates or updates a team.
func (s *teamsState) PutTeam(id, name string, members []string) (Team, bool, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return Team{}, false, errors.New("server: a team needs an id")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, existed := s.teams[id]
	team := Team{ID: id, Name: name, Members: members, At: s.now().UnixMilli()}
	if existed {
		team.At = s.teams[id].At
	}
	s.teams[id] = team
	return team, !existed, nil
}

// RemoveTeam deletes a team, and the groups that belonged to it.
func (s *teamsState) RemoveTeam(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.teams[id]; !ok {
		return false
	}
	delete(s.teams, id)
	for groupID, group := range s.groups {
		if strings.EqualFold(group.Team, id) {
			delete(s.groups, groupID)
		}
	}
	return true
}

// Groups lists the conversations, ordered by id.
func (s *teamsState) Groups() []Group {
	s.mu.Lock()
	defer s.mu.Unlock()
	groups := make([]Group, 0, len(s.groups))
	for _, group := range s.groups {
		groups = append(groups, group)
	}
	slices.SortStableFunc(groups, func(a, b Group) int { return cmp.Compare(a.ID, b.ID) })
	return groups
}

// PutGroup creates or updates a conversation.
func (s *teamsState) PutGroup(id, title, team string, members []string) (Group, bool, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return Group{}, false, errors.New("server: a group needs an id")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	existing, existed := s.groups[id]
	group := Group{ID: id, Title: title, Team: team, Members: members, At: s.now().UnixMilli()}
	if existed {
		if title == "" {
			group.Title = existing.Title
		}
		if team == "" {
			group.Team = existing.Team
		}
		if members == nil {
			group.Members = existing.Members
		}
		group.At = existing.At
	}
	s.groups[id] = group
	return group, !existed, nil
}

// RemoveGroup deletes a conversation.
func (s *teamsState) RemoveGroup(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.groups[id]; !ok {
		return false
	}
	delete(s.groups, id)
	return true
}

// Hosts lists the devices, ordered by name.
func (s *teamsState) Hosts() []Host {
	s.mu.Lock()
	defer s.mu.Unlock()
	hosts := make([]Host, 0, len(s.hosts))
	for _, host := range s.hosts {
		hosts = append(hosts, host)
	}
	slices.SortStableFunc(hosts, func(a, b Host) int { return cmp.Compare(a.Name, b.Name) })
	return hosts
}

// BeginPairing mints a short-lived code for a device the operator names.
func (s *teamsState) BeginPairing(name string) (pairCode, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return pairCode{}, errors.New("server: a device needs a name")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// Expired codes are swept here so repeated pairings cannot grow the map
	// without bound.
	for existing, pending := range s.pairs {
		if s.now().After(pending.expiresAt) {
			delete(s.pairs, existing)
		}
	}
	code, err := randomCode()
	if err != nil {
		return pairCode{}, err
	}
	pair := pairCode{code: code, name: name, expiresAt: s.now().Add(2 * time.Minute)}
	s.pairs[pair.code] = pair
	return pair, nil
}

// CompletePairing registers the device that presents a live code.
func (s *teamsState) CompletePairing(code string) (Host, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	pending, ok := s.pairs[strings.TrimSpace(code)]
	if !ok {
		return Host{}, errors.New("server: no such pairing code")
	}
	if s.now().After(pending.expiresAt) {
		delete(s.pairs, pending.code)
		return Host{}, errors.New("server: the pairing code expired")
	}
	delete(s.pairs, pending.code)
	host := s.hosts[pending.name]
	host.Name = pending.name
	host.PairedAt = s.now().UnixMilli()
	s.hosts[pending.name] = host
	return host, nil
}

// RemoveHost forgets a device. The local host cannot be removed: it is where the
// environment lives.
func (s *teamsState) RemoveHost(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	host, ok := s.hosts[strings.TrimSpace(name)]
	if !ok {
		return errors.New("server: no such host")
	}
	if host.Local {
		return errors.New("server: the local host cannot be removed")
	}
	delete(s.hosts, host.Name)
	return nil
}

// SetDefaultHost marks one device as the one used when a task names none.
func (s *teamsState) SetDefaultHost(name string) (Host, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	host, ok := s.hosts[strings.TrimSpace(name)]
	if !ok {
		return Host{}, errors.New("server: no such host")
	}
	for existing, candidate := range s.hosts {
		candidate.Default = existing == host.Name
		s.hosts[existing] = candidate
	}
	return s.hosts[host.Name], nil
}

// configHosts renders the state back into configuration, so a change survives a
// restart.
func (s *teamsState) configHosts() []HostConfig {
	hosts := []HostConfig{}
	for _, host := range s.Hosts() {
		hosts = append(hosts, HostConfig{Name: host.Name, Local: host.Local, Default: host.Default})
	}
	return hosts
}

// randomCode is a short human-typeable pairing code.
func randomCode() (string, error) {
	const alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	random := make([]byte, 8)
	if _, err := cryptoRead(random); err != nil {
		// A fixed fallback code would be guessable, so a failure is reported
		// instead of pairing with a known value.
		return "", fmt.Errorf("server: generate a pairing code: %w", err)
	}
	code := make([]byte, len(random))
	for index, value := range random {
		code[index] = alphabet[int(value)%len(alphabet)]
	}
	return string(code), nil
}

// cryptoRead is injectable so a test can make a pairing code predictable.
var cryptoRead = func(buffer []byte) (int, error) {
	return rand.Read(buffer)
}
