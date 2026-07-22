//go:build darwin || windows

package main

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

type desktopCandidate struct {
	Identity       brandIdentity
	Slug           string
	HomeDir        string
	AppPath        string
	ExecutablePath string
	DescriptorPath string
}

type desktopDiscoveryDeps struct {
	glob     func(string) ([]string, error)
	readFile func(string) ([]byte, error)
	exists   func(string) bool
}

var productionDesktopDiscoveryDeps = desktopDiscoveryDeps{
	glob:     filepath.Glob,
	readFile: os.ReadFile,
	exists:   statExists,
}

func desktopDescriptorPatterns(goos string, env func(string) string, home string) []string {
	if goos == "windows" {
		var patterns []string
		for _, key := range []string{"LOCALAPPDATA", "PROGRAMFILES", "PROGRAMFILES(X86)"} {
			root := env(key)
			if root == "" {
				continue
			}
			patterns = append(
				patterns,
				filepath.Join(root, "Programs", "*", "resources", "brand.json"),
				filepath.Join(root, "*", "resources", "brand.json"),
			)
		}
		return patterns
	}

	patterns := []string{
		filepath.Join("/Applications", "*.app", "Contents", "Resources", "brand.json"),
	}
	if home != "" {
		patterns = append(
			patterns,
			filepath.Join(home, "Applications", "*.app", "Contents", "Resources", "brand.json"),
		)
	}
	return patterns
}

func discoverDesktopCandidates(
	goos string,
	env func(string) string,
	home string,
	deps desktopDiscoveryDeps,
) ([]desktopCandidate, error) {
	pathKey := func(path string) string {
		path = filepath.Clean(path)
		if goos == "windows" {
			return strings.ToLower(path)
		}
		return path
	}

	var descriptorPaths []string
	seenDescriptors := make(map[string]struct{})
	for _, pattern := range desktopDescriptorPatterns(goos, env, home) {
		matches, err := deps.glob(pattern)
		if err != nil {
			return nil, fmt.Errorf("glob desktop descriptors %q: %w", pattern, err)
		}
		for _, path := range matches {
			key := pathKey(path)
			if _, seen := seenDescriptors[key]; seen {
				continue
			}
			seenDescriptors[key] = struct{}{}
			descriptorPaths = append(descriptorPaths, path)
		}
	}

	candidates := make([]desktopCandidate, 0, len(descriptorPaths)+1)
	seenApps := make(map[string]struct{})
	descriptorOwners := make(map[string]struct{}, len(descriptorPaths))
	for _, descriptorPath := range descriptorPaths {
		descriptorOwners[pathKey(desktopOwnerForDescriptor(goos, descriptorPath))] = struct{}{}
		data, err := deps.readFile(descriptorPath)
		if err != nil {
			return nil, fmt.Errorf("read desktop descriptor %q: %w", descriptorPath, err)
		}
		doc, ok := parseBrandDescriptor(data)
		if !ok {
			slog.Debug("desktop descriptor rejected",
				"descriptor", descriptorPath,
				"reason", "validation failed")
			continue
		}

		id := identityFromDisplayName(doc.DisplayName)
		appPath, executablePath := desktopPathsForDescriptor(goos, descriptorPath, id)
		if goos == "darwin" && filepath.Base(appPath) != id.MacAppName {
			slog.Debug("desktop descriptor rejected",
				"descriptor", descriptorPath,
				"reason", "bundle name does not match display name")
			continue
		}
		if !deps.exists(executablePath) {
			slog.Debug("desktop descriptor rejected",
				"descriptor", descriptorPath,
				"reason", "expected executable missing")
			continue
		}

		key := pathKey(appPath)
		if _, seen := seenApps[key]; seen {
			continue
		}
		seenApps[key] = struct{}{}
		candidates = append(candidates, desktopCandidate{
			Identity:       id,
			Slug:           doc.Slug,
			HomeDir:        doc.HomeDir,
			AppPath:        appPath,
			ExecutablePath: executablePath,
			DescriptorPath: descriptorPath,
		})
	}

	legacyID := defaultBrandIdentity()
	if legacyPath := installedAppPath(goos, legacyID, env, home, deps.exists); legacyPath != "" {
		key := pathKey(legacyPath)
		ownerKey := key
		if goos == "windows" {
			ownerKey = pathKey(filepath.Dir(legacyPath))
		}
		_, hasDescriptor := descriptorOwners[ownerKey]
		if _, represented := seenApps[key]; !represented && !hasDescriptor {
			candidates = append(candidates, desktopCandidate{
				Identity:       legacyID,
				Slug:           "otto",
				HomeDir:        ".otto",
				AppPath:        legacyPath,
				ExecutablePath: desktopExecutablePath(goos, legacyPath, legacyID),
			})
		}
	}

	return candidates, nil
}

func desktopExecutablePath(goos, appPath string, id brandIdentity) string {
	if goos == "windows" {
		return appPath
	}
	return filepath.Join(appPath, "Contents", "MacOS", id.DisplayName)
}

func desktopPathsForDescriptor(goos, descriptorPath string, id brandIdentity) (appPath, executablePath string) {
	appPath = desktopOwnerForDescriptor(goos, descriptorPath)
	if goos == "windows" {
		executablePath = filepath.Join(appPath, id.WinExeName)
		return executablePath, executablePath
	}

	executablePath = filepath.Join(appPath, "Contents", "MacOS", id.DisplayName)
	return appPath, executablePath
}

func desktopOwnerForDescriptor(goos, descriptorPath string) string {
	if goos == "windows" {
		return filepath.Dir(filepath.Dir(descriptorPath))
	}
	return filepath.Dir(filepath.Dir(filepath.Dir(descriptorPath)))
}

// desktopAppCandidates returns launchable-path candidates, most-preferred
// first. Pure (branches on goos) so both OSes are tested on one box.
func desktopAppCandidates(goos string, id brandIdentity, env func(string) string, home string) []string {
	switch goos {
	case "windows":
		var out []string
		for _, k := range []string{"LOCALAPPDATA", "PROGRAMFILES", "PROGRAMFILES(X86)"} {
			if root := env(k); root != "" {
				out = append(
					out,
					filepath.Join(root, "Programs", id.DisplayName, id.WinExeName),
					filepath.Join(root, id.DisplayName, id.WinExeName),
				)
			}
		}
		return out
	default: // darwin
		return []string{
			filepath.Join("/Applications", id.MacAppName),
			filepath.Join(home, "Applications", id.MacAppName),
		}
	}
}

// installedAppPath returns the first existing candidate, or "".
func installedAppPath(goos string, id brandIdentity, env func(string) string, home string, exists func(string) bool) string {
	for _, c := range desktopAppCandidates(goos, id, env, home) {
		if exists(c) {
			return c
		}
	}
	return ""
}
