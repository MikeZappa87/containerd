/*
   Copyright The containerd Authors.

   Licensed under the Apache License, Version 2.0 (the "License");
   you may not use this file except in compliance with the License.
   You may obtain a copy of the License at

       http://www.apache.org/licenses/LICENSE-2.0

   Unless required by applicable law or agreed to in writing, software
   distributed under the License is distributed on an "AS IS" BASIS,
   WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
   See the License for the specific language governing permissions and
   limitations under the License.
*/

package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// ---------- scanDir tests ----------

func writePluginFile(t *testing.T, dir, name string, reg grpcPluginRegistration) {
	t.Helper()
	data, err := json.Marshal(reg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), data, 0644); err != nil {
		t.Fatal(err)
	}
}

func TestScanDir_Empty(t *testing.T) {
	dir := t.TempDir()
	s := &grpcPluginSyncer{confDir: dir}

	regs, err := s.scanDir()
	if err != nil {
		t.Fatal(err)
	}
	if len(regs) != 0 {
		t.Fatalf("expected 0 registrations, got %d", len(regs))
	}
}

func TestScanDir_ParsesValidFile(t *testing.T) {
	dir := t.TempDir()
	s := &grpcPluginSyncer{confDir: dir}

	writePluginFile(t, dir, "10-calico.json", grpcPluginRegistration{
		Name:    "calico",
		Address: "/run/calico.sock",
		Primary: true,
	})

	regs, err := s.scanDir()
	if err != nil {
		t.Fatal(err)
	}
	if len(regs) != 1 {
		t.Fatalf("expected 1 registration, got %d", len(regs))
	}
	if regs[0].Name != "calico" {
		t.Fatalf("expected name calico, got %s", regs[0].Name)
	}
	if regs[0].Address != "/run/calico.sock" {
		t.Fatalf("expected address /run/calico.sock, got %s", regs[0].Address)
	}
	if !regs[0].Primary {
		t.Fatal("expected primary=true")
	}
	// Priority should default to 100.
	if regs[0].Priority != 100 {
		t.Fatalf("expected default priority 100, got %d", regs[0].Priority)
	}
}

func TestScanDir_SkipsInvalidJSON(t *testing.T) {
	dir := t.TempDir()
	s := &grpcPluginSyncer{confDir: dir}

	// Write broken JSON.
	if err := os.WriteFile(filepath.Join(dir, "bad.json"), []byte("{not json"), 0644); err != nil {
		t.Fatal(err)
	}
	// Write valid file alongside.
	writePluginFile(t, dir, "good.json", grpcPluginRegistration{
		Name:    "good",
		Address: "/run/good.sock",
	})

	regs, err := s.scanDir()
	if err != nil {
		t.Fatal(err)
	}
	if len(regs) != 1 {
		t.Fatalf("expected 1 valid registration, got %d", len(regs))
	}
	if regs[0].Name != "good" {
		t.Fatalf("expected name good, got %s", regs[0].Name)
	}
}

func TestScanDir_SkipsMissingAddress(t *testing.T) {
	dir := t.TempDir()
	s := &grpcPluginSyncer{confDir: dir}

	writePluginFile(t, dir, "no-addr.json", grpcPluginRegistration{
		Name: "no-addr",
	})

	regs, err := s.scanDir()
	if err != nil {
		t.Fatal(err)
	}
	if len(regs) != 0 {
		t.Fatalf("expected 0 registrations (address required), got %d", len(regs))
	}
}

func TestScanDir_IgnoresNonJSONFiles(t *testing.T) {
	dir := t.TempDir()
	s := &grpcPluginSyncer{confDir: dir}

	// Write a .toml file — should be ignored.
	if err := os.WriteFile(filepath.Join(dir, "plugin.toml"), []byte(`name = "x"`), 0644); err != nil {
		t.Fatal(err)
	}
	writePluginFile(t, dir, "ok.json", grpcPluginRegistration{
		Name:    "ok",
		Address: "/run/ok.sock",
	})

	regs, err := s.scanDir()
	if err != nil {
		t.Fatal(err)
	}
	if len(regs) != 1 {
		t.Fatalf("expected 1 registration, got %d", len(regs))
	}
}

func TestScanDir_DefaultsNameToFilename(t *testing.T) {
	dir := t.TempDir()
	s := &grpcPluginSyncer{confDir: dir}

	writePluginFile(t, dir, "20-flannel.json", grpcPluginRegistration{
		Address: "/run/flannel.sock",
	})

	regs, err := s.scanDir()
	if err != nil {
		t.Fatal(err)
	}
	if regs[0].Name != "20-flannel.json" {
		t.Fatalf("expected name to default to filename, got %s", regs[0].Name)
	}
}

// ---------- Sort order tests ----------

func TestScanDir_SortPrimaryFirst(t *testing.T) {
	dir := t.TempDir()
	s := &grpcPluginSyncer{confDir: dir}

	writePluginFile(t, dir, "a-secondary.json", grpcPluginRegistration{
		Name:     "secondary",
		Address:  "/run/secondary.sock",
		Priority: 1,
	})
	writePluginFile(t, dir, "b-primary.json", grpcPluginRegistration{
		Name:    "primary",
		Address: "/run/primary.sock",
		Primary: true,
	})

	regs, err := s.scanDir()
	if err != nil {
		t.Fatal(err)
	}
	if len(regs) != 2 {
		t.Fatalf("expected 2 registrations, got %d", len(regs))
	}
	// Primary should be first regardless of priority or filename.
	if regs[0].Name != "primary" {
		t.Fatalf("expected primary first, got %s", regs[0].Name)
	}
	if regs[1].Name != "secondary" {
		t.Fatalf("expected secondary second, got %s", regs[1].Name)
	}
}

func TestScanDir_SortByPriority(t *testing.T) {
	dir := t.TempDir()
	s := &grpcPluginSyncer{confDir: dir}

	writePluginFile(t, dir, "a.json", grpcPluginRegistration{
		Name:     "low-prio",
		Address:  "/run/a.sock",
		Priority: 200,
	})
	writePluginFile(t, dir, "b.json", grpcPluginRegistration{
		Name:     "high-prio",
		Address:  "/run/b.sock",
		Priority: 10,
	})
	writePluginFile(t, dir, "c.json", grpcPluginRegistration{
		Name:     "mid-prio",
		Address:  "/run/c.sock",
		Priority: 50,
	})

	regs, err := s.scanDir()
	if err != nil {
		t.Fatal(err)
	}
	if len(regs) != 3 {
		t.Fatalf("expected 3 registrations, got %d", len(regs))
	}
	if regs[0].Name != "high-prio" {
		t.Fatalf("expected high-prio first, got %s", regs[0].Name)
	}
	if regs[1].Name != "mid-prio" {
		t.Fatalf("expected mid-prio second, got %s", regs[1].Name)
	}
	if regs[2].Name != "low-prio" {
		t.Fatalf("expected low-prio third, got %s", regs[2].Name)
	}
}

func TestScanDir_SortByNameAsTiebreaker(t *testing.T) {
	dir := t.TempDir()
	s := &grpcPluginSyncer{confDir: dir}

	writePluginFile(t, dir, "z.json", grpcPluginRegistration{
		Name:    "zebra",
		Address: "/run/z.sock",
	})
	writePluginFile(t, dir, "a.json", grpcPluginRegistration{
		Name:    "alpha",
		Address: "/run/a.sock",
	})

	regs, err := s.scanDir()
	if err != nil {
		t.Fatal(err)
	}
	// Same default priority (100), so sorted by name.
	if regs[0].Name != "alpha" {
		t.Fatalf("expected alpha first, got %s", regs[0].Name)
	}
	if regs[1].Name != "zebra" {
		t.Fatalf("expected zebra second, got %s", regs[1].Name)
	}
}

// ---------- Registration file schema tests ----------

func TestRegistrationJSON_AllFields(t *testing.T) {
	raw := `{
		"name": "calico",
		"address": "/run/calico.sock",
		"primary": true,
		"priority": 5,
		"dial_timeout": 10
	}`

	var reg grpcPluginRegistration
	if err := json.Unmarshal([]byte(raw), &reg); err != nil {
		t.Fatal(err)
	}
	if reg.Name != "calico" || reg.Address != "/run/calico.sock" || !reg.Primary || reg.Priority != 5 || reg.DialTimeout != 10 {
		t.Fatalf("unexpected parsed value: %+v", reg)
	}
}

func TestRegistrationJSON_MinimalFields(t *testing.T) {
	raw := `{"address": "/run/mynet.sock"}`

	var reg grpcPluginRegistration
	if err := json.Unmarshal([]byte(raw), &reg); err != nil {
		t.Fatal(err)
	}
	if reg.Address != "/run/mynet.sock" {
		t.Fatalf("expected address, got %s", reg.Address)
	}
	if reg.Primary {
		t.Fatal("primary should default to false")
	}
	if reg.Priority != 0 {
		t.Fatal("priority should default to 0 before scanDir normalizes it")
	}
}

// ---------- newGRPCPluginSyncer tests ----------

func TestNewGRPCPluginSyncer_CreatesDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "subdir", "net.d")

	syncer, err := newGRPCPluginSyncer(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer syncer.stop()

	// Verify directory was created.
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("expected directory to be created: %v", err)
	}
	if !info.IsDir() {
		t.Fatal("expected a directory")
	}
}

func TestNewGRPCPluginSyncer_EmptyDirNoError(t *testing.T) {
	dir := t.TempDir()

	syncer, err := newGRPCPluginSyncer(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer syncer.stop()

	// With empty dir, plugin() should return a DynamicPodNetworkPlugin
	// whose Current() is nil.
	p := syncer.plugin()
	if p == nil {
		t.Fatal("plugin() should return non-nil DynamicPodNetworkPlugin")
	}
	if p.Current() != nil {
		t.Fatal("expected nil current with empty directory")
	}
}

func TestNewGRPCPluginSyncer_StopClosesWatcher(t *testing.T) {
	dir := t.TempDir()

	syncer, err := newGRPCPluginSyncer(dir)
	if err != nil {
		t.Fatal(err)
	}

	if err := syncer.stop(); err != nil {
		t.Fatalf("stop should not error: %v", err)
	}
}

func TestGRPCPluginSyncer_LastStatus(t *testing.T) {
	dir := t.TempDir()
	syncer, err := newGRPCPluginSyncer(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer syncer.stop()

	// Initially no error (empty dir → successful "no plugins" load).
	if syncer.lastStatus() != nil {
		t.Fatalf("expected nil lastStatus, got %v", syncer.lastStatus())
	}
}
