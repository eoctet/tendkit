package handler

import (
	"context"
	"errors"
	"io/fs"
	"slices"
	"strings"
	"testing"

	"github.com/eoctet/tendkit/internal/model"
	runtimeutil "github.com/eoctet/tendkit/pkg/runtime"
)

type rubyRunner struct {
	run func(context.Context, string) (runtimeutil.Result, error)
}

func (r rubyRunner) Run(c context.Context, s string, _ map[string]string) (runtimeutil.Result, error) {
	return r.run(c, s)
}
func newRuby(r Runner, ruby, gem string) *RubyHandler {
	h := NewRuby(r)
	h.lookPath = func(s string) (string, error) {
		if s == "ruby" && ruby != "" {
			return ruby, nil
		}
		if s == "gem" && gem != "" {
			return gem, nil
		}
		return "", ErrNotFound
	}
	h.stat = func(string) (fs.FileInfo, error) { return fixtureFileInfo{}, nil }
	h.homeDir = func() (string, error) { return "/home", nil }
	return h
}
func TestRubyHandlerGoldenAndCommands(t *testing.T) {
	if command := rubyGemListCommand("ruby"); !strings.Contains(command, "version: s.version.to_s") || strings.Contains(command, "version: s.to_s") {
		t.Fatalf("RubyGems JSON version expression = %q", command)
	}
	r := rubyRunner{run: func(_ context.Context, _ string) (runtimeutil.Result, error) {
		return runtimeutil.Result{Stdout: `[{"name":"user_gem","version":"1.0.0","summary":" user ","source":"https://github.com/acme/user.git","install_path":"/u","install_scope":"user","bindir":"/bin","executable_names":["user-gem"],"executables":["/bin/user-gem"]},{"name":"system","version":"2.0.0","homepage":"https://github.com/acme/system","install_path":"/s","install_scope":"system"},{"name":"unknown","version":"3.0.0","install_path":"/x","install_scope":"unknown"}]`}, nil
	}}
	var p []Progress
	result := newRuby(r, "/ruby", "/gem").Scan(context.Background(), Request{Report: func(v Progress) { p = append(p, v) }})
	if !result.Complete || len(result.Candidates) != 3 {
		t.Fatalf("result=%#v", result)
	}
	for i, c := range result.Candidates {
		if c.Application.ID != "pkg-ruby-"+[]string{"user-gem", "system", "unknown"}[i] || c.Application.Identity != "package:ruby:"+[]string{"usergem", "system", "unknown"}[i] || c.Application.Provider.Type != model.ProviderDefault || c.Aliases[0] != "ruby:"+c.Application.Name {
			t.Fatalf("candidate=%#v", c)
		}
	}
	if result.Candidates[0].Application.UpdateMode != model.ModeAuto || !strings.Contains(result.Candidates[0].Application.Provider.UpdateAction(), "--user-install") || result.Candidates[1].Application.UpdateMode != model.ModeAuto || result.Candidates[2].Application.UpdateMode != model.ModeCheck || result.Candidates[0].Application.Description != "user" || result.Candidates[1].Application.URL != "https://github.com/acme/system" {
		t.Fatalf("apps=%#v", result.Candidates)
	}
	if result.Candidates[0].Evidence == nil || result.Candidates[0].Evidence.Source != "ruby" || !slices.Equal(result.Candidates[0].Evidence.ExecutablePaths, []string{"/bin/user-gem"}) {
		t.Fatalf("evidence=%#v", result.Candidates[0].Evidence)
	}
	if len(p) != 4 || p[0].Stage != model.ScanStagePackageList || p[1].Stage != model.ScanStageApplication {
		t.Fatalf("progress=%#v", p)
	}
	for _, candidate := range result.Candidates {
		assertActiveProvider(t, candidate.Application.Provider.Type)
	}
	command := rubyGemListCommand("/ruby")
	if !strings.Contains(command, "File.realpath") || !strings.Contains(command, "default_gem?") || !strings.Contains(command, "source_code_uri") {
		t.Fatal(command)
	}
}
func TestRubyHandlerFailuresAndFallback(t *testing.T) {
	for _, tc := range []struct {
		out string
		err error
	}{{"bad", nil}, {"", errors.New("failed")}} {
		result := newRuby(rubyRunner{run: func(_ context.Context, _ string) (runtimeutil.Result, error) {
			return runtimeutil.Result{Stdout: tc.out}, tc.err
		}}, "/ruby", "/gem").Scan(context.Background(), Request{})
		if result.Complete || result.Err == nil {
			t.Fatalf("result=%#v", result)
		}
	}
	empty := newRuby(rubyRunner{run: func(_ context.Context, _ string) (runtimeutil.Result, error) {
		return runtimeutil.Result{Stdout: "[]"}, nil
	}}, "/ruby", "/gem").Scan(context.Background(), Request{})
	if !empty.Complete {
		t.Fatal(empty)
	}
	h := newRuby(rubyRunner{run: func(_ context.Context, _ string) (runtimeutil.Result, error) {
		return runtimeutil.Result{Stdout: "[]"}, nil
	}}, "", "")
	h.stat = func(p string) (fs.FileInfo, error) {
		if p == "/home/bin/ruby" || p == "/home/bin/gem" {
			return fixtureFileInfo{}, nil
		}
		return nil, ErrNotFound
	}
	if result := h.Scan(context.Background(), Request{Configured: []model.Application{{Name: "Ruby", InstallPath: "~/bin/ruby"}, {Name: "RubyGems", InstallPath: "~/bin/gem"}}}); !result.Complete {
		t.Fatal(result)
	}
}

func TestRubyHandlerFallbackUsesAbsoluteRubyGemRunnerActions(t *testing.T) {
	h := newRuby(rubyRunner{run: func(_ context.Context, _ string) (runtimeutil.Result, error) {
		return runtimeutil.Result{Stdout: `[{"name":"tool","version":"1.0.0","install_path":"/u","install_scope":"user"}]`}, nil
	}}, "", "")
	h.stat = func(path string) (fs.FileInfo, error) {
		if path == "/home/bin/ruby" {
			return fixtureFileInfo{}, nil
		}
		return nil, ErrNotFound
	}
	result := h.Scan(context.Background(), Request{Configured: []model.Application{{Name: "Ruby", InstallPath: "~/bin/ruby"}}})
	if !result.Complete || len(result.Candidates) != 1 {
		t.Fatalf("result=%#v", result)
	}
	for _, command := range []string{result.Candidates[0].Application.Provider.VersionAction(), result.Candidates[0].Application.Provider.CheckAction(), result.Candidates[0].Application.Provider.UpdateAction()} {
		if !strings.HasPrefix(command, "/home/bin/ruby -rrubygems/gem_runner -e ") || !strings.Contains(command, "Gem::GemRunner.new.run(ARGV)") || strings.Contains(command, " -S gem") {
			t.Fatalf("unexpected RubyGems action %q", command)
		}
	}
}

func TestRubyHandlerRejectsRelativeConfiguredRuby(t *testing.T) {
	h := newRuby(rubyRunner{run: func(_ context.Context, _ string) (runtimeutil.Result, error) {
		return runtimeutil.Result{Stdout: "[]"}, nil
	}}, "", "")
	if result := h.Scan(context.Background(), Request{Configured: []model.Application{{Name: "Ruby", InstallPath: "bin/ruby"}}}); result.Complete || result.Err == nil {
		t.Fatalf("relative configured Ruby accepted: %#v", result)
	}
}

func TestRubyGemInventoryCommandUsesStableVersionTieBreakAndSort(t *testing.T) {
	command := rubyGemListCommand("ruby")
	for _, fragment := range []string{
		"s.version == old.version && s.full_gem_path < old.full_gem_path",
		"latest.values.sort_by { |s| [s.name.downcase, s.name, s.full_gem_path] }",
		`scope == "user" ? Gem.bindir(user_dir) : Gem.bindir`,
	} {
		if !strings.Contains(command, fragment) {
			t.Fatalf("inventory command missing stable ordering contract %q: %s", fragment, command)
		}
	}
}
