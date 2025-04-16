// Copyright 2013-2023 The NATS Authors
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package graft

import (
	"bytes"
	"crypto/sha1"
	"encoding/json"
	"os"
)

type envelope struct {
	SHA, Data []byte
}
type persistentState struct {
	CurrentTerm uint64
	VotedFor    string
}

func (n *Node) initLog(path string) error {
	if log, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0660); err != nil {
		return err
	} else {
		log.Close()
	}

	n.logPath = path

	ps, err := n.readState(path)
	if err != nil && err != ErrLogNoState {
		return err
	}

	if ps != nil {
		n.setTerm(ps.CurrentTerm)
		n.setVote(ps.VotedFor)
	}

	return nil
}

func (n *Node) closeLog() error {
	err := os.Remove(n.logPath)
	n.logPath = ""
	return err
}

func (n *Node) writeState() error {
	n.mu.Lock()
	ps := persistentState{
		CurrentTerm: n.term,
		VotedFor:    n.vote,
	}
	logPath := n.logPath
	n.mu.Unlock()

	buf, err := json.Marshal(ps)
	if err != nil {
		return err
	}

	sha := sha1.Sum(buf)
	// Set a SHA1 to test for corruption on read
	env := envelope{
		SHA:  sha[:],
		Data: buf,
	}
	toWrite, err := json.Marshal(env)
	if err != nil {
		return err
	}

	return os.WriteFile(logPath, toWrite, 0660)
}

func (n *Node) readState(path string) (*persistentState, error) {
	buf, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(buf) <= 0 {
		return nil, ErrLogNoState
	}

	env := &envelope{}
	if err := json.Unmarshal(buf, env); err != nil {
		return nil, err
	}

	// Test for corruption
	sha := sha1.Sum(env.Data)
	if !bytes.Equal(sha[:], env.SHA) {
		// Data didn't match the expected digest. Fall back to the old behavior.
		// Prior to https://github.com/nats-io/graft/pull/39, this code wrongly
		// used sha1.New().Sum(env.Data).
		// This mimics that functionality, while being a bit easier to follow.
		hashOfNothing := sha1.Sum(nil)
		legacyDigest := append(bytes.Clone(env.Data), hashOfNothing[:]...)

		if !bytes.Equal(legacyDigest, env.SHA) {
			return nil, ErrLogCorrupt
		}
	}

	ps := &persistentState{}
	if err := json.Unmarshal(env.Data, ps); err != nil {
		return nil, err
	}
	return ps, nil
}
