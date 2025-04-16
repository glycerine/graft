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
	"testing"
	"time"
)

func TestLogPermissions(t *testing.T) {
	ci := ClusterInfo{Name: "foo", Size: 3}
	hand, rpc, log := genNodeArgs(t)
	// remove it
	os.Remove(log)
	tmpDir := t.TempDir()
	file, _ := os.CreateTemp(tmpDir, "_log")
	os.Chmod(tmpDir, 0400)

	defer file.Close()
	defer os.RemoveAll(tmpDir)
	defer os.Chmod(tmpDir, 0770)

	// Test we get correct error
	if _, err := New(ci, hand, rpc, file.Name()); err == nil {
		t.Fatal("Expected an error with bad permissions")
	}
}

func TestLogCleanupOnClose(t *testing.T) {
	ci := ClusterInfo{Name: "foo", Size: 3}
	hand, rpc, log := genNodeArgs(t)
	node, err := New(ci, hand, rpc, log)
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}
	node.Close()
	if _, err := os.Stat(log); !os.IsNotExist(err) {
		t.Fatal("Expected log to be removed on Close()")
	}
}

func TestLogPresenceOnNew(t *testing.T) {
	// Make sure to clean us up from wonly state
	defer mockResetPeers()

	ci := ClusterInfo{Name: "p", Size: 1}
	hand, rpc, log := genNodeArgs(t)
	node, err := New(ci, hand, rpc, log)
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}
	defer node.Close()

	// Set some non default values
	node.setTerm(10)
	node.setVote("fake")
	// Force writing the state
	if err := node.writeState(); err != nil {
		t.Fatalf("Unexpected error writing state: %v", err)
	}

	// Wait to become leader..
	if state := waitForState(node, LEADER); state != LEADER {
		t.Fatalf("Expected Node to be Leader, got %s", state)
	}

	// Create another with the same log..
	node2, err := New(ci, hand, rpc, log)
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}
	defer node2.Close()

	if node.term != node2.term {
		t.Fatalf("Terms did not match %d vs %d\n", node.term, node2.term)
	}
	if node.vote != node2.vote {
		t.Fatalf("Votes did not match %s vs %s\n", node.vote, node2.vote)
	}
}

func TestLogCreationOnNew(t *testing.T) {
	ci := ClusterInfo{Name: "foo", Size: 3}
	hand, rpc, log := genNodeArgs(t)
	node, err := New(ci, hand, rpc, log)
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}
	defer node.Close()

	fake := fakeNode("fake")
	mockRegisterPeer(fake)
	defer mockUnregisterPeer(fake.Id())

	// Should move to candidate state
	if state := waitForState(node, CANDIDATE); state != CANDIDATE {
		t.Fatalf("Expected node to move to Candidate state, got: %s", state)
	}
	// After this point, we only have the guarantee that the node's state
	// changed to Candidate, but it is possible that the runAsCandidate()
	// loop has not started yet, or is in progress but before the state was
	// written. We know that the state is written before sending a vote request,
	// so look for that vote request as the indication that the state should
	// have been written.
	<-fake.VoteRequests
	// We should have written our state.
	testStateOfNode(t, node)
}

func TestCorruption(t *testing.T) {
	ci := ClusterInfo{Name: "foo", Size: 3}
	hand, rpc, log := genNodeArgs(t)
	node, err := New(ci, hand, rpc, log)
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}
	defer node.Close()

	// Delay elections
	node.mu.Lock()
	node.electTimer.Reset(10 * time.Second)
	node.mu.Unlock()

	node.setTerm(1)
	node.setVote("foo")
	// Force writing the state
	node.writeState()

	// We should have written our state.
	testStateOfNode(t, node)

	// Now introduce some corruption
	buf, err := os.ReadFile(node.logPath)
	if err != nil {
		t.Fatalf("Could not read logfile: %v", err)
	}
	env := &envelope{}
	if err := json.Unmarshal(buf, env); err != nil {
		t.Fatalf("Error unmarshalling envelope: %v", err)
	}
	env.Data = []byte("ZZZZ")
	toWrite, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("Error Marshaling envelope: %v", err)
	}

	if err := os.WriteFile(node.logPath, toWrite, 0660); err != nil {
		t.Fatalf("Error writing envelope: %v", err)
	}

	// Make sure we get the corruptError
	_, err = node.readState(node.logPath)
	if err == nil {
		t.Fatal("Expected an error reading corrupt state")
	}
	if err != ErrLogCorrupt {
		t.Fatalf("Expected corrupt error, got %q", err)
	}
}

func TestVerification(t *testing.T) {
	ci := ClusterInfo{Name: "foo", Size: 3}
	hand, rpc, log := genNodeArgs(t)
	node, err := New(ci, hand, rpc, log)
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}
	defer node.Close()

	// Delay elections
	node.mu.Lock()
	node.electTimer.Reset(10 * time.Second)
	node.mu.Unlock()

	node.setTerm(1)
	node.setVote("foo")
	// Force writing the state
	node.writeState()

	// We should have written our state.
	testStateOfNode(t, node)

	// Now introduce some corruption
	buf, err := os.ReadFile(node.logPath)
	if err != nil {
		t.Fatalf("Could not read logfile: %v", err)
	}
	env := &envelope{}
	if err := json.Unmarshal(buf, env); err != nil {
		t.Fatalf("Error unmarshalling envelope: %v", err)
	}

	sha := sha1.Sum(env.Data)
	if have, want := env.SHA, sha[:]; !bytes.Equal(have, want) {
		t.Errorf("node.writeState().SHA = %x; want %x", have, want)
	}

	// Make sure we get the corruptError
	_, err = node.readState(node.logPath)
	if err != nil {
		t.Fatal("Unexpected error reading corrupt state")
	}
}

func TestVerificationFallback(t *testing.T) {
	ci := ClusterInfo{Name: "foo", Size: 3}
	hand, rpc, log := genNodeArgs(t)
	node, err := New(ci, hand, rpc, log)
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}
	defer node.Close()

	// Delay elections
	node.mu.Lock()
	node.electTimer.Reset(10 * time.Second)
	node.mu.Unlock()

	node.setTerm(1)
	node.setVote("foo")
	// Force writing the state
	node.writeState()

	// We should have written our state.
	testStateOfNode(t, node)

	// Now introduce some corruption
	buf, err := os.ReadFile(node.logPath)
	if err != nil {
		t.Fatalf("Could not read logfile: %v", err)
	}
	env := &envelope{}
	if err := json.Unmarshal(buf, env); err != nil {
		t.Fatalf("Error unmarshalling envelope: %v", err)
	}

	hashOfNothing := sha1.Sum(nil)
	env.SHA = append(bytes.Clone(env.Data), hashOfNothing[:]...)

	toWrite, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("Error Marshaling envelope: %v", err)
	}

	if err := os.WriteFile(node.logPath, toWrite, 0660); err != nil {
		t.Fatalf("Error writing envelope: %v", err)
	}

	// Make sure we get the corruptError
	_, err = node.readState(node.logPath)
	if err != nil {
		t.Fatal("Unexpected error reading corrupt state")
	}
}

// This will test that we have the correct saved state at any point in time.
func testStateOfNode(t *testing.T, node *Node) {
	if node == nil {
		stackFatalf(t, "Expected a non-nil Node")
	}
	ps, err := node.readState(node.logPath)
	if err != nil {
		stackFatalf(t, "Err reading state: %q\n", err)
	}
	if ps.CurrentTerm != node.CurrentTerm() {
		stackFatalf(t, "Expected CurrentTerm of %d, got %d\n",
			node.CurrentTerm(), ps.CurrentTerm)
	}
	if ps.VotedFor != node.CurrentVote() {
		stackFatalf(t, "Expected a vote for %q, got %q\n",
			node.CurrentVote(), ps.VotedFor)
	}
}
