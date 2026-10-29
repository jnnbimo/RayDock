// Package store keeps nodes, groups and settings in a JSON file.
package store

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"raydock/internal/node"
	"raydock/internal/sysproxy"
	"raydock/internal/xray"
)

// DefaultGroup names the group created for nodes added without one.
const DefaultGroup = "默认分组"

// Group holds nodes. A group with a URL is a subscription, whose nodes are replaced on every update.
type Group struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	URL  string `json:"url,omitempty"`
	// Front and Back are node IDs: the group's nodes are dialed through Front, and traffic leaves through Back.
	Front string `json:"front,omitempty"`
	Back  string `json:"back,omitempty"`

	UpdatedAt time.Time `json:"updatedAt,omitzero"`
	Count     int       `json:"count,omitempty"`
	Skipped   int       `json:"skipped,omitempty"`   // entries the last successful update could not use
	LastError string    `json:"lastError,omitempty"` // why the last update failed; its nodes are the ones before
}

func (g *Group) Subscription() bool { return g.URL != "" }

// Settings are the user settings; the Xray options are embedded in the same JSON object.
type Settings struct {
	xray.Options
	SystemProxy bool `json:"systemProxy"` // set the system proxy while connected
	AutoConnect bool `json:"autoConnect"` // connect to the last node at start
	Appearance  int  `json:"appearance"`  // 0 system, 1 light, 2 dark
}

func DefaultSettings() Settings {
	return Settings{Options: xray.DefaultOptions(), SystemProxy: true, Appearance: 2}
}

// ProxyBackup records the system proxy before RayDock took it over and the addresses it wrote.
// It is saved when the proxy is enabled and cleared after restoring, so a crash can be recovered.
type ProxyBackup struct {
	Previous sysproxy.Settings `json:"previous"`
	Applied  string            `json:"applied"` // the address last written
	// Pending is the address being written, saved before it is: a write that failed or did not finish
	// may have left it or Applied in place.
	Pending string `json:"pending,omitempty"`
}

// Owns reports whether system proxy server addr is one RayDock wrote, or was writing.
func (b *ProxyBackup) Owns(addr string) bool {
	return addr != "" && (addr == b.Applied || addr == b.Pending)
}

type State struct {
	Nodes       []node.Node  `json:"nodes"`
	Groups      []Group      `json:"groups"`
	Settings    Settings     `json:"settings"`
	SelectedID  string       `json:"selectedId,omitempty"` // the row picked in the node list
	LastID      string       `json:"lastId,omitempty"`     // the node last connected to, which connecting again uses
	ProxyBackup *ProxyBackup `json:"proxyBackup,omitempty"`
	// Ports are dedicated local ports by node ID: each always proxies through its node, whatever is connected.
	Ports map[string]int `json:"ports,omitempty"`
	// GeoTag is the rule data release installed: by updating it, or from the installer; empty for rule data
	// of unknown release (the official core's own, or an older installer's).
	GeoTag string `json:"geoTag,omitempty"`
}

// Load reads path, returning defaults when it does not exist. Missing fields keep their defaults.
func Load(path string) (State, error) {
	st := State{Settings: DefaultSettings()}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return st, nil
	}
	if err != nil {
		return st, err
	}
	if err := json.Unmarshal(data, &st); err != nil {
		return State{Settings: DefaultSettings()}, err
	}
	def := DefaultSettings()
	if st.Settings.Port == 0 {
		st.Settings.Port = def.Port
	}
	if st.Settings.Mode == "" {
		st.Settings.Mode = def.Mode
	}
	if st.Settings.LogLevel == "" {
		st.Settings.LogLevel = def.LogLevel
	}
	migrateGroups(&st, data)
	return st, nil
}

// migrateGroups maps legacy subscriptions and ungrouped nodes to groups.
func migrateGroups(st *State, data []byte) {
	var legacy struct {
		Subs  []Group `json:"subscriptions"`
		Nodes []struct {
			SubID string `json:"subId"`
		} `json:"nodes"`
	}
	_ = json.Unmarshal(data, &legacy)
	if st.Groups == nil {
		st.Groups = legacy.Subs
	}
	manual := ""
	for i := range st.Nodes {
		n := &st.Nodes[i]
		if n.GroupID != "" {
			continue
		}
		if i < len(legacy.Nodes) && legacy.Nodes[i].SubID != "" {
			n.GroupID = legacy.Nodes[i].SubID
			continue
		}
		if manual == "" {
			manual = node.NewID()
			st.Groups = append(st.Groups, Group{ID: manual, Name: DefaultGroup})
		}
		n.GroupID = manual
	}
}

// Save writes a temporary file and renames it, so a crash never leaves a corrupt file.
func Save(path string, st State) error { return writeJSON(path, st) }

// Traffic is the traffic of each day by its local date ("2006-01-02"), which LoadTraffic and SaveTraffic
// keep in a file of its own: it changes every second traffic flows, settings rarely.
type Traffic struct {
	Days map[string]xray.Traffic `json:"days"`
}

// LoadTraffic reads path; a file that does not exist is no traffic yet.
func LoadTraffic(path string) (Traffic, error) {
	var t Traffic
	data, err := os.ReadFile(path)
	if err == nil {
		err = json.Unmarshal(data, &t)
	} else if errors.Is(err, fs.ErrNotExist) {
		err = nil
	}
	if err != nil {
		return Traffic{}, err
	}
	return t, nil
}

func SaveTraffic(path string, t Traffic) error { return writeJSON(path, t) }

func writeJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer os.Remove(tmp)
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
