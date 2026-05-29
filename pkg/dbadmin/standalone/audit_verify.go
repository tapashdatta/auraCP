package standalone

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
)

// VerifyResult describes a chain verification outcome.
type VerifyResult struct {
	OK             bool
	EventsScanned  int
	HeadsScanned   int
	BreakLine      int    // line number (1-based) of first mismatch; 0 if OK
	BreakEventID   string // event_id where the break starts
	ExpectedPrev   string // what the file claims for PrevEventHash
	ComputedPrev   string // what we computed walking the file
}

// VerifyAuditLog walks path line by line, recomputes the SHA-256 chain,
// and returns the first divergence. Chainhead lines are skipped for the
// chain check (they're attestation, not chain links).
func VerifyAuditLog(path string) (*VerifyResult, error) {
	return VerifyAuditLogFrom(path, 0)
}

// VerifyAuditLogFrom is like VerifyAuditLog but starts at line
// startLine (1-based; 0 = from beginning). Useful for resuming a
// previously-interrupted check.
func VerifyAuditLogFrom(path string, startLine int) (*VerifyResult, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 16*1024*1024)
	var res VerifyResult
	res.ComputedPrev = GenesisPrevHash
	line := 0
	for sc.Scan() {
		line++
		if line < startLine {
			continue
		}
		b := sc.Bytes()
		if len(b) == 0 {
			continue
		}
		if isChainHead(b) {
			res.HeadsScanned++
			continue
		}
		// Peek the event's PrevEventHash and event_id without
		// committing to a full Event marshal — the verifier only
		// needs two fields.
		var peek struct {
			EventID       string `json:"event_id"`
			PrevEventHash string `json:"prev_event_hash"`
		}
		if err := json.Unmarshal(b, &peek); err != nil {
			return nil, fmt.Errorf("audit verify: line %d unmarshal: %w", line, err)
		}
		if peek.PrevEventHash != res.ComputedPrev {
			res.BreakLine = line
			res.BreakEventID = peek.EventID
			res.ExpectedPrev = peek.PrevEventHash
			return &res, nil
		}
		// Advance the running hash.
		sum := sha256.Sum256(b)
		res.ComputedPrev = hex.EncodeToString(sum[:])
		res.EventsScanned++
	}
	if err := sc.Err(); err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	res.OK = true
	return &res, nil
}

// AuditReader iterates events from an audit log. Replay tooling uses it
// to feed events into alternative sinks. NextEvent returns io.EOF when
// the file is exhausted.
type AuditReader struct {
	f  *os.File
	sc *bufio.Scanner
}

// OpenAuditReader opens path for line-by-line consumption.
func OpenAuditReader(path string) (*AuditReader, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 16*1024*1024)
	return &AuditReader{f: f, sc: sc}, nil
}

// Close releases the underlying file.
func (r *AuditReader) Close() error { return r.f.Close() }

// NextEvent returns the next non-chainhead event line as parsed JSON.
// Returns io.EOF on exhaustion. Chainhead lines are skipped transparently.
func (r *AuditReader) NextEvent() (map[string]any, error) {
	for r.sc.Scan() {
		b := r.sc.Bytes()
		if len(b) == 0 {
			continue
		}
		if isChainHead(b) {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal(b, &m); err != nil {
			return nil, err
		}
		return m, nil
	}
	if err := r.sc.Err(); err != nil {
		return nil, err
	}
	return nil, io.EOF
}
