package checkpoint

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

const CatalogFile = "storage.yaml"

var validName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,63}$`)
var validLayer = regexp.MustCompile(`^disk-[a-f0-9]{32}\.vhdx$`)
var validNode = regexp.MustCompile(`^[a-f0-9]{32}$`)

// Entry names an immutable recovery point; writable state always lives in a separate leaf.
type Entry struct {
	ID       string   `yaml:"id"`
	Name     string   `yaml:"name"`
	Created  int64    `yaml:"created"`
	Disk     string   `yaml:"disk"`
	Parent   string   `yaml:"parent,omitempty"`
	Legacy   bool     `yaml:"legacy,omitempty"`
	State    string   `yaml:"state,omitempty"`
	Adapters []string `yaml:"adapters,omitempty"`
	MACs     []string `yaml:"macs,omitempty"`
}

type Fork struct {
	Entry Entry  `yaml:"snapshot"`
	Child string `yaml:"child"`
}

type Restore struct {
	Active    string `yaml:"previous_active"`
	Current   string `yaml:"previous_current,omitempty"`
	Firmware  bool   `yaml:"restore_firmware"`
	Backup    string `yaml:"firmware_backup,omitempty"`
	Rollback  *Entry `yaml:"previous_state,omitempty"`
	Reverting bool   `yaml:"reverting,omitempty"`
}

type Store struct {
	Version   int               `yaml:"version"`
	Active    string            `yaml:"active"`
	Current   string            `yaml:"current,omitempty"`
	Layers    map[string]string `yaml:"layers"`
	Entries   []Entry           `yaml:"snapshots"`
	Pending   *Fork             `yaml:"pending_fork,omitempty"`
	Restoring *Restore          `yaml:"pending_restore,omitempty"`
	Garbage   []string          `yaml:"garbage,omitempty"`
	Dir       string            `yaml:"-"`
}

func NewID() (string, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(id[:]), nil
}
func (s *Store) Path(name string) string { return filepath.Join(s.Dir, filepath.FromSlash(name)) }
func NewEntry(name string) (Entry, error) {
	id, err := NewID()
	return Entry{ID: id, Name: name, Created: time.Now().Unix()}, err
}
func Load(dir string) (*Store, error) {
	s := &Store{Version: 1, Active: "disk.vhdx", Layers: map[string]string{"disk.vhdx": ""}, Dir: dir}
	data, err := os.ReadFile(filepath.Join(dir, CatalogFile))
	if err == nil {
		s = &Store{Dir: dir}
		dec := yaml.NewDecoder(bytes.NewReader(data))
		dec.KnownFields(true)
		if err = dec.Decode(s); err != nil {
			return nil, fmt.Errorf("读取磁盘链清单失败（不会回退到基础盘）: %w", err)
		}
		var extra any
		if err = dec.Decode(&extra); err != io.EOF {
			return nil, errors.New("磁盘链清单必须只包含一份 YAML 文档")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err = s.Validate(); err != nil {
		return nil, err
	}
	if err = s.importLegacy(); err != nil {
		return nil, err
	}
	if err = s.Validate(); err != nil {
		return nil, err
	}
	return s, nil
}
func (s *Store) importLegacy() error {
	dirs, err := os.ReadDir(s.Path("snapshots"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	known := map[string]bool{}
	for _, e := range s.Entries {
		known[e.ID] = true
	}
	for _, dir := range dirs {
		if !dir.IsDir() || !validName.MatchString(dir.Name()) || known[dir.Name()] {
			continue
		}
		disk := "snapshots/" + dir.Name() + "/disk.vhdx"
		if _, tracked := s.Layers[disk]; tracked || slices.Contains(s.Garbage, disk) {
			continue
		}
		info, err := os.Stat(s.Path(disk))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return errors.New("旧快照磁盘必须是普通文件")
		}
		s.Layers[disk] = ""
		s.Entries = append(s.Entries, Entry{ID: dir.Name(), Name: dir.Name(), Created: info.ModTime().Unix(), Disk: disk, Legacy: true})
	}
	return nil
}
func safeFile(name string) bool {
	if name == "disk.vhdx" || validLayer.MatchString(name) {
		return true
	}
	parts := strings.Split(name, "/")
	return len(parts) == 3 && parts[0] == "snapshots" && validName.MatchString(parts[1]) && parts[2] == "disk.vhdx"
}
func (s *Store) Validate() error {
	if s.Version != 1 || s.Layers == nil || !safeFile(s.Active) {
		return errors.New("磁盘链清单版本或活动路径无效")
	}
	if _, ok := s.Layers[s.Active]; !ok {
		return errors.New("活动磁盘不在磁盘链清单中")
	}
	names, entries := map[string]bool{}, map[string]Entry{}
	for _, e := range s.Entries {
		if !validName.MatchString(e.Name) || names[strings.ToLower(e.Name)] {
			return errors.New("快照名称无效或重复")
		}
		if err := s.validateEntry(e); err != nil {
			return err
		}
		if _, ok := entries[e.ID]; ok {
			return errors.New("快照 ID 重复")
		}
		names[strings.ToLower(e.Name)], entries[e.ID] = true, e
	}
	if s.Current != "" {
		if _, ok := entries[s.Current]; !ok {
			return errors.New("当前快照节点不存在")
		}
	}
	parents := make(map[string]string, len(entries))
	for id, entry := range entries {
		parents[id] = entry.Parent
	}
	if err := validateLinks(parents, "快照树"); err != nil {
		return err
	}
	children := make(map[string][]string, len(s.Layers))
	for disk, parent := range s.Layers {
		if !safeFile(disk) || parent != "" && !safeFile(parent) {
			return errors.New("磁盘链包含无效路径")
		}
		children[parent] = append(children[parent], disk)
	}
	if err := validateLinks(s.Layers, "磁盘链"); err != nil {
		return err
	}
	// Every descendant of the writable disk is unsafe as a recovery point.
	writable := map[string]bool{s.Active: true}
	pending := []string{s.Active}
	for len(pending) > 0 {
		disk := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		for _, child := range children[disk] {
			writable[child] = true
			pending = append(pending, child)
		}
	}
	for _, e := range s.Entries {
		if writable[e.Disk] {
			return errors.New("活动磁盘仍被快照引用，拒绝写入只读恢复点")
		}
	}
	if s.Pending != nil {
		if err := s.validateEntry(s.Pending.Entry); err != nil {
			return err
		}
		if !validName.MatchString(s.Pending.Entry.Name) || names[strings.ToLower(s.Pending.Entry.Name)] {
			return errors.New("未完成分叉的快照名称无效或重复")
		}
		if _, ok := entries[s.Pending.Entry.ID]; ok {
			return errors.New("未完成分叉的快照 ID 重复")
		}
		if s.Pending.Entry.Parent != s.Current {
			return errors.New("未完成分叉的父节点无效")
		}
		if !validLayer.MatchString(s.Pending.Child) || s.Layers[s.Pending.Child] != s.Pending.Entry.Disk {
			return errors.New("未完成分叉记录无效")
		}
		if s.Pending.Entry.Disk != s.Active {
			return errors.New("未完成分叉与活动磁盘不一致")
		}
	}
	if s.Restoring != nil {
		r := s.Restoring
		if s.Pending != nil || !safeFile(r.Active) || (r.Active == s.Active) != r.Reverting {
			return errors.New("未完成恢复的原磁盘无效")
		}
		if _, ok := s.Layers[r.Active]; !ok {
			return errors.New("未完成恢复的原磁盘不在磁盘链中")
		}
		if r.Current != "" {
			if _, ok := entries[r.Current]; !ok {
				return errors.New("未完成恢复的原快照不存在")
			}
		}
		if r.Rollback != nil {
			if err := s.validateEntry(*r.Rollback); err != nil {
				return err
			}
			if r.Rollback.Disk != r.Active || (r.Rollback.State != "running" && r.Rollback.State != "paused") || r.Backup != "state-"+r.Rollback.ID+".vmgs" || !r.Firmware {
				return errors.New("未完成恢复的原运行现场无效")
			}
		}
		if r.Backup != "" && (!r.Firmware || !validStateFile.MatchString(r.Backup) || !strings.HasSuffix(r.Backup, ".vmgs")) {
			return errors.New("未完成恢复的固件备份路径无效")
		}
	}
	live := s.liveFiles()
	for _, name := range s.Garbage {
		if !safeArtifact(name) || live[name] {
			return errors.New("待清理路径无效或仍在使用")
		}
	}
	return nil
}
func validateLinks(parents map[string]string, label string) error {
	visited := make(map[string]uint8, len(parents))
	for start := range parents {
		node := start
		for node != "" && visited[node] == 0 {
			parent, ok := parents[node]
			if !ok {
				return errors.New(label + "缺少父节点")
			}
			visited[node] = 1
			node = parent
		}
		if node != "" && visited[node] == 1 {
			return errors.New(label + "存在循环")
		}
		for node = start; node != "" && visited[node] == 1; node = parents[node] {
			visited[node] = 2
		}
	}
	return nil
}

// CheckFork limits actual VHDX ancestry, including hidden parents of deleted snapshots.
func (s *Store) CheckFork(parent string) error {
	const maxDepth = 64
	for depth := 0; parent != ""; depth++ {
		if depth >= maxDepth-1 {
			return errors.New("磁盘差分链已达 64 层，不能继续分叉；请克隆为独立磁盘")
		}
		next, ok := s.Layers[parent]
		if !ok {
			return errors.New("磁盘链缺少父节点")
		}
		parent = next
	}
	return nil
}

func (s *Store) validateEntry(e Entry) error {
	if !validNode.MatchString(e.ID) && !validName.MatchString(e.ID) {
		return errors.New("快照 ID 无效")
	}
	if e.State != "" && e.State != "off" && e.State != "running" && e.State != "paused" {
		return errors.New("快照运行状态无效")
	}
	if e.State != "" && !validNode.MatchString(e.ID) {
		return errors.New("状态快照 ID 无效")
	}
	if _, ok := s.Layers[e.Disk]; !ok {
		return errors.New("快照磁盘不在磁盘链清单中")
	}
	return nil
}
func (s *Store) Find(name string) (Entry, bool) {
	for _, e := range s.Entries {
		if strings.EqualFold(e.Name, name) {
			return e, true
		}
	}
	return Entry{}, false
}
func (s *Store) Tree() []Entry {
	entries := append([]Entry(nil), s.Entries...)
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].Created < entries[j].Created })
	children := make(map[string][]Entry, len(entries))
	for _, entry := range entries {
		children[entry.Parent] = append(children[entry.Parent], entry)
	}
	var result []Entry
	stack := append([]Entry(nil), children[""]...)
	slices.Reverse(stack)
	for len(stack) > 0 {
		entry := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		result = append(result, entry)
		next := children[entry.ID]
		for i := len(next) - 1; i >= 0; i-- {
			stack = append(stack, next[i])
		}
	}
	return result
}
func (s *Store) Remove(id string) {
	var parent string
	for _, e := range s.Entries {
		if e.ID == id {
			parent = e.Parent
		}
	}
	s.Entries = slices.DeleteFunc(s.Entries, func(e Entry) bool { return e.ID == id })
	for i := range s.Entries {
		if s.Entries[i].Parent == id {
			s.Entries[i].Parent = parent
		}
	}
	if s.Current == id {
		s.Current = parent
	}
}
func (s *Store) CommitFork() {
	pending := s.Pending
	s.Active, s.Current = pending.Child, pending.Entry.ID
	s.Entries = append(s.Entries, pending.Entry)
	s.Pending = nil
}

// Save publishes one flushed manifest. Uncommitted layers never replace the active disk.
func (s *Store) Save() error {
	if err := s.Validate(); err != nil {
		return err
	}
	var node yaml.Node
	if err := node.Encode(s); err != nil {
		return err
	}
	comments := map[string]string{
		"version": "Storage manifest format version.", "active": "Writable VHDX leaf relative to this machine directory.",
		"current": "Recovery point from which the writable state descends.", "layers": "Disk parent links; an empty value denotes an independent disk.",
		"snapshots": "Immutable named recovery points, arranged by parent ID.", "pending_fork": "Write-ahead journal for an interrupted backing-file switch.",
		"garbage": "Unreferenced files to remove after committing this manifest.", "id": "Internal stable snapshot identifier.",
		"name": "Unique user-visible recovery-point name.", "created": "Creation time in Unix seconds.", "disk": "Read-only recovery-point disk leaf.",
		"parent": "Parent recovery-point ID; absent for a root.", "legacy": "Imported full-copy snapshot without firmware or RAM.",
		"snapshot": "Recovery point being created.", "child": "Proposed writable differencing leaf.",
		"pending_restore": "Interrupted disk and firmware restore; roll back before a cold boot or cleanup.",
		"previous_active": "Original writable disk retained until restore commits.", "previous_current": "Original parent recovery point.",
		"restore_firmware": "Whether this restore replaces firmware; an absent backup means no original firmware existed.",
		"firmware_backup":  "Original firmware retained until successful restore or rollback.",
		"previous_state":   "Original RAM, device and firmware state retained until restore commits.",
		"reverting":        "Original disk selected; reload the original RAM before clearing the restore journal.",
		"state":            "Saved power state; running and paused include RAM and devices; absent means legacy disk only.",
		"adapters":         "Stable saved virtual NIC device identifiers, in enabled NIC order.",
		"macs":             "Saved endpoint MAC addresses, in the same NIC order.",
	}
	var annotate func(*yaml.Node)
	annotate = func(n *yaml.Node) {
		if n.Kind == yaml.MappingNode {
			for i := 0; i < len(n.Content); i += 2 {
				n.Content[i].HeadComment = comments[n.Content[i].Value]
			}
		}
		for _, child := range n.Content {
			annotate(child)
		}
	}
	annotate(&node)
	data, err := yaml.Marshal(&node)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(s.Dir, ".storage-")
	if err != nil {
		return err
	}
	path := f.Name()
	defer os.Remove(path)
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return publishFile(path, s.Path(CatalogFile))
}

func (s *Store) CheckFiles(names ...string) error {
	for _, name := range names {
		if !safeArtifact(name) {
			return errors.New("虚拟机存储路径无效")
		}
		path := s.Dir
		parts := strings.Split(name, "/")
		for i, part := range parts {
			path = filepath.Join(path, part)
			info, err := os.Lstat(path)
			if err != nil {
				return err
			}
			if info.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("虚拟机磁盘不能使用符号链接: %s", path)
			}
			if i == len(parts)-1 && !info.Mode().IsRegular() {
				return errors.New("虚拟机磁盘必须是普通文件")
			}
		}
	}
	return nil
}
func (s *Store) Chain() []string {
	var paths []string
	for name := s.Active; name != ""; name = s.Layers[name] {
		paths = append(paths, name)
	}
	return paths
}
