// Command privacycheck screens public Git content without echoing offending values.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type finding struct {
	Location string `json:"location"`
	Reason   string `json:"reason"`
}

type scanner struct {
	root      string
	findings  []finding
	inspected int
}

func (s *scanner) git(ctx context.Context, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, "git", args...)
	command.Dir = s.root
	data, err := command.Output()
	if err != nil {
		return nil, errors.New("cannot inspect Git repository")
	}
	return data, nil
}

func (s *scanner) inspect(name string, data []byte) {
	s.inspected++
	if reason := privatePath(name); reason != "" {
		s.findings = append(s.findings, finding{Location: name, Reason: reason})
	}
	for _, reason := range contentFindings(name, data) {
		s.findings = append(s.findings, finding{Location: name, Reason: reason})
	}
}

func (s *scanner) index(ctx context.Context) error {
	data, err := s.git(ctx, "ls-files", "-z")
	if err != nil {
		return err
	}
	for _, entry := range bytes.Split(data, []byte{0}) {
		if len(entry) == 0 {
			continue
		}
		name := string(entry)
		if reason := privatePath(name); reason == "unsafe repository path" {
			s.findings = append(s.findings, finding{Location: name, Reason: reason})
			continue
		}
		filename := filepath.Join(s.root, filepath.FromSlash(name))
		info, err := os.Lstat(filename)
		if err != nil {
			return errors.New("cannot inspect a tracked file")
		}
		if !info.Mode().IsRegular() {
			s.findings = append(s.findings, finding{Location: name, Reason: "non-regular tracked file"})
			continue
		}
		content, err := os.ReadFile(filename)
		if err != nil {
			return errors.New("cannot read a tracked file")
		}
		s.inspect(name, content)
	}
	return nil
}

func (s *scanner) metadata(name string, data []byte) {
	for _, reason := range append(contentFindings("git-metadata", data), identityFindings(data)...) {
		s.findings = append(s.findings, finding{Location: name, Reason: reason})
	}
}

func (s *scanner) history(ctx context.Context) error {
	refs, err := s.git(ctx, "rev-list", "--all")
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, commit := range strings.Fields(string(refs)) {
		data, err := s.git(ctx, "cat-file", "commit", commit)
		if err != nil {
			return err
		}
		s.metadata(commit, data)
		tree, err := s.git(ctx, "ls-tree", "--full-tree", "-rz", commit)
		if err != nil {
			return err
		}
		for _, entry := range bytes.Split(tree, []byte{0}) {
			if len(entry) == 0 {
				continue
			}
			meta, name, ok := strings.Cut(string(entry), "\t")
			if !ok {
				return errors.New("unexpected Git tree output")
			}
			parts := strings.Fields(meta)
			if len(parts) != 3 {
				return errors.New("unexpected Git tree entry")
			}
			key := parts[2] + "\x00" + name
			if seen[key] {
				continue
			}
			seen[key] = true
			if parts[0] == "120000" || parts[1] != "blob" {
				s.findings = append(s.findings, finding{Location: name, Reason: "symlink or submodule in history"})
				continue
			}
			content, err := s.git(ctx, "cat-file", "blob", parts[2])
			if err != nil {
				return err
			}
			s.inspect(name, content)
		}
	}
	tags, err := s.git(ctx, "for-each-ref", "--format=%(objecttype) %(objectname)", "refs/tags")
	if err != nil {
		return err
	}
	for _, line := range strings.Split(strings.TrimSpace(string(tags)), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || fields[0] != "tag" {
			continue
		}
		data, err := s.git(ctx, "cat-file", "tag", fields[1])
		if err != nil {
			return err
		}
		s.metadata(fields[1], data)
	}
	return nil
}

func run(ctx context.Context, args []string) int {
	flags := flag.NewFlagSet("privacycheck", flag.ContinueOnError)
	history := flags.Bool("history", false, "inspect all reachable committed objects and tag metadata")
	report := flags.String("report", "", "write location/category details to a private local JSON file")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	root, err := os.Getwd()
	if err != nil {
		return 1
	}
	s := scanner{root: root, findings: []finding{}}
	rootData, err := s.git(ctx, "rev-parse", "--show-toplevel")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	s.root = strings.TrimSpace(string(rootData))
	if *history {
		err = s.history(ctx)
	} else {
		err = s.index(ctx)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if *report != "" {
		data, marshalErr := json.MarshalIndent(s.findings, "", "  ")
		if marshalErr != nil {
			return 1
		}
		if err := os.WriteFile(*report, append(data, '\n'), 0o600); err != nil {
			fmt.Fprintln(os.Stderr, "cannot write private report")
			return 1
		}
	}
	for _, item := range s.findings {
		sum := sha256.Sum256([]byte(item.Location))
		fmt.Fprintln(os.Stderr, "privacy violation:", item.Reason, "location-sha256:", hex.EncodeToString(sum[:])[:12])
	}
	if len(s.findings) > 0 {
		return 1
	}
	fmt.Printf("Privacy check passed (%d file versions inspected).\n", s.inspected)
	return 0
}

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	code := run(ctx, os.Args[1:])
	cancel()
	os.Exit(code)
}
