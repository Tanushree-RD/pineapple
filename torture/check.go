package main

// The checker: compares what the database holds after a crash (the dump) with what the oracle
// recorded, using the per-key rule (SPEC 14.4) and the commit-order rules (SPEC 14.5).
//
// Why a short prefix check is enough (and no linearizability solver):
//   - There is ONE committer, and serve prints each ACK from the committer before the next
//     transaction starts. So the order ACK lines arrive in IS the commit order.
//   - Therefore, per key, the surviving value must come from the ACKed op with the highest
//     ack_seq (or from the baseline if no op on that key was ACKed). Anything else loses
//     an acknowledged write.
//   - The only legal deviation is the ONE transaction the committer was inside when the process
//     died. It is later in commit order than every ACK, so if it is present it must show its
//     effect on every key it touches (atomicity) and win on all of them.
//   - Two or more unACKed transactions present, or a partial one, is a failure.

import (
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
)

// StateKind says what a key holds.
type StateKind uint8

const (
	KeyAbsent StateKind = iota
	KeyValue            // a HYDRA value naming OpID
	KeyEmpty            // present with an empty value (no op id can be read from it)
)

// KeyState is the observable state of one key.
type KeyState struct {
	Kind StateKind
	OpID uint64 // KeyValue only
	Size int    // KeyValue only
}

func (s KeyState) String() string {
	switch s.Kind {
	case KeyAbsent:
		return "absent"
	case KeyEmpty:
		return "empty value"
	}
	return fmt.Sprintf("op %d (%d bytes)", s.OpID, s.Size)
}

func sameState(a, b KeyState) bool {
	if a.Kind != b.Kind {
		return false
	}
	if a.Kind == KeyValue {
		return a.OpID == b.OpID && a.Size == b.Size
	}
	return true
}

// Baseline is the state verified after the previous iteration. Absent keys are not stored.
type Baseline map[string]KeyState

// Failure is one violated rule, with enough detail to debug it (SPEC 15.4).
type Failure struct {
	Key      []byte
	Reason   string
	Observed KeyState
	Expected string   // what the rules required
	Allowed  []string // allowed op ids with their SENT/ACK times
}

func (f Failure) String() string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "key=%s: %s\n", hexKey(f.Key), f.Reason)
	fmt.Fprintf(&sb, "      observed: %s\n", f.Observed)
	if f.Expected != "" {
		fmt.Fprintf(&sb, "      required: %s\n", f.Expected)
	}
	for _, a := range f.Allowed {
		fmt.Fprintf(&sb, "      allowed:  %s\n", a)
	}
	return strings.TrimRight(sb.String(), "\n")
}

func hexKey(k []byte) string {
	if len(k) > 40 {
		return hex.EncodeToString(k[:40]) + "..."
	}
	return hex.EncodeToString(k)
}

// CheckResult is the verdict for one iteration.
type CheckResult struct {
	Failures []Failure
	Observed map[string]KeyState // every present key (becomes the next baseline when OK)
	Surprise *Op                 // the one unACKed transaction found present, if any
	SeenOps  map[uint64]bool     // op ids that explain the observed state
	Acked    int
	InFlight int
	Rejected int
}

// OK reports whether the iteration passed.
func (r *CheckResult) OK() bool { return len(r.Failures) == 0 }

// stateOfFunc lets the checker explain an unexpected op id (REJECTED, DEAD, from an old iteration).
type stateOfFunc func(id uint64) (OpState, bool)

// effects returns the final effect of an op on each key it touches. The last part on a key wins
// (SPEC 4.4), so PUT X then DEL X is absent and DEL X then PUT X is present.
func effects(op *Op) map[string]KeyState {
	m := make(map[string]KeyState, len(op.Parts))
	for _, p := range op.Parts {
		switch {
		case p.Del:
			m[string(p.Key)] = KeyState{Kind: KeyAbsent}
		case p.Size == 0:
			m[string(p.Key)] = KeyState{Kind: KeyEmpty}
		default:
			m[string(p.Key)] = KeyState{Kind: KeyValue, OpID: op.ID, Size: p.Size}
		}
	}
	return m
}

// CheckIteration verifies one iteration. base is the previous verified state, ops are this
// iteration's ops (every state), dump is the database's content after recovery, and stateOf
// explains op ids from earlier iterations.
func CheckIteration(base Baseline, ops []*Op, dump map[string][]byte, stateOf stateOfFunc) *CheckResult {
	res := &CheckResult{Observed: map[string]KeyState{}, SeenOps: map[uint64]bool{}}
	fail := func(f Failure) { res.Failures = append(res.Failures, f) }

	// 0. Sanity of the oracle's own data (SPEC 14.5 rule 3, real time). The reply reader stamps
	// ACK events after the request was invoked, so this can only fail if HYDRA itself is wrong.
	seenAckSeq := map[uint64]*Op{}
	for _, op := range ops {
		switch op.State {
		case StateAcked:
			res.Acked++
			if op.AckEvent <= op.InvokeSeq {
				fail(Failure{Reason: fmt.Sprintf("oracle bug: op %d ACKed (event %d) before it was invoked (event %d)", op.ID, op.AckEvent, op.InvokeSeq)})
			}
			if other, dup := seenAckSeq[op.AckSeq]; dup {
				fail(Failure{Reason: fmt.Sprintf("oracle bug: ops %d and %d share ack_seq %d", other.ID, op.ID, op.AckSeq)})
			}
			seenAckSeq[op.AckSeq] = op
		case StateInFlight:
			res.InFlight++
		case StateRejected:
			res.Rejected++
		}
	}

	// 1. Decode and verify every observed value. A value that does not verify is torn, mixed,
	// belongs to another key, or is garbage: rule 4 of the invariant (SPEC 1).
	bad := map[string]bool{} // keys whose stored value failed verification (already reported)
	for ks, v := range dump {
		key := []byte(ks)
		info, err := DecodeValue(key, v)
		if err != nil {
			bad[ks] = true
			fail(Failure{Key: key, Reason: "stored value fails verification: " + err.Error(), Observed: KeyState{Kind: KeyValue}})
			continue
		}
		if info.Empty {
			res.Observed[ks] = KeyState{Kind: KeyEmpty}
		} else {
			res.Observed[ks] = KeyState{Kind: KeyValue, OpID: info.OpID, Size: info.Size}
		}
	}

	// 2. Per-key expectations from ACKed ops (the one with the highest ack_seq wins) and the
	// in-flight candidates.
	eff := make(map[*Op]map[string]KeyState, len(ops))
	lastAcked := map[string]*Op{}
	inflightOn := map[string][]*Op{}
	touched := map[string]struct{}{}
	for _, op := range ops {
		e := effects(op)
		eff[op] = e
		for k := range e {
			touched[k] = struct{}{}
			switch op.State {
			case StateAcked:
				if cur := lastAcked[k]; cur == nil || op.AckSeq > cur.AckSeq {
					lastAcked[k] = op
				}
			case StateInFlight:
				inflightOn[k] = append(inflightOn[k], op)
			}
		}
	}

	keys := map[string]struct{}{}
	for k := range base {
		keys[k] = struct{}{}
	}
	for k := range touched {
		keys[k] = struct{}{}
	}
	for k := range dump {
		keys[k] = struct{}{}
	}
	sortedKeys := make([]string, 0, len(keys))
	for k := range keys {
		sortedKeys = append(sortedKeys, k)
	}
	sort.Strings(sortedKeys)

	expected := func(k string) (KeyState, string) {
		if op := lastAcked[k]; op != nil {
			return eff[op][k], fmt.Sprintf("the effect of ACKed op %d (ack_seq %d, the last ACK on this key)", op.ID, op.AckSeq)
		}
		return base[k], "the baseline (state verified after the previous iteration); no op on this key was ACKed"
	}

	// 3. Compare. A key that already matches needs nothing more. A key that does not match can
	// only be explained by an in-flight op whose effect equals what we observed.
	required := map[string][]*Op{}
	for _, k := range sortedKeys {
		if bad[k] {
			continue // already reported as an unverifiable value
		}
		obs := res.Observed[k] // zero value = absent
		want, why := expected(k)
		if sameState(obs, want) {
			continue
		}
		var cands []*Op
		for _, op := range inflightOn[k] {
			if sameState(obs, eff[op][k]) {
				cands = append(cands, op)
			}
		}
		if len(cands) == 0 {
			fail(Failure{
				Key: []byte(k), Observed: obs, Expected: want.String() + " (" + why + ")",
				Reason:  diagnose(obs, want, k, ops, lastAcked, base, stateOf),
				Allowed: allowedList(k, want, ops, lastAcked, inflightOn),
			})
			continue
		}
		required[k] = cands
	}

	// 4. At most ONE unACKed transaction may be present, and it must be complete.
	if len(required) > 0 {
		res.Surprise = pickSurprise(required, eff, res.Observed, fail)
	}

	// Which op ids explain what we saw (for DEAD retirement).
	for k, st := range res.Observed {
		if st.Kind == KeyValue {
			res.SeenOps[st.OpID] = true
		} else if st.Kind == KeyEmpty {
			if op := lastAcked[k]; op != nil {
				res.SeenOps[op.ID] = true
			}
		}
	}
	return res
}

// pickSurprise finds the single in-flight op that explains every key that deviated from the
// ACK-derived expectation, and checks it is complete. It reports failures through fail.
func pickSurprise(required map[string][]*Op, eff map[*Op]map[string]KeyState, obs map[string]KeyState, fail func(Failure)) *Op {
	reqKeys := make([]string, 0, len(required))
	for k := range required {
		reqKeys = append(reqKeys, k)
	}
	sort.Strings(reqKeys)

	// Intersect the candidate sets.
	common := map[*Op]bool{}
	for _, op := range required[reqKeys[0]] {
		common[op] = true
	}
	for _, k := range reqKeys[1:] {
		next := map[*Op]bool{}
		for _, op := range required[k] {
			if common[op] {
				next[op] = true
			}
		}
		common = next
	}
	if len(common) == 0 {
		var parts []string
		for _, k := range reqKeys {
			var ids []string
			for _, op := range required[k] {
				ids = append(ids, fmt.Sprintf("op %d", op.ID))
			}
			parts = append(parts, fmt.Sprintf("key %s needs %s", hexKey([]byte(k)), strings.Join(ids, " or ")))
		}
		fail(Failure{
			Key: []byte(reqKeys[0]), Observed: obs[reqKeys[0]],
			Reason: "more than one unACKed transaction is present (at most one is allowed: the one the committer was inside at the kill): " + strings.Join(parts, "; "),
		})
		return nil
	}

	cands := make([]*Op, 0, len(common))
	for op := range common {
		cands = append(cands, op)
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].ID < cands[j].ID })

	var firstProblem *Failure
	for _, op := range cands {
		if prob := incomplete(op, eff[op], obs); prob == nil {
			return op
		} else if firstProblem == nil {
			firstProblem = prob
		}
	}
	fail(*firstProblem)
	return nil
}

// incomplete checks that an unACKed op that is present shows its effect on EVERY key it touches.
func incomplete(op *Op, e map[string]KeyState, obs map[string]KeyState) *Failure {
	var present, missing []string
	var firstMissing string
	keys := make([]string, 0, len(e))
	for k := range e {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if sameState(obs[k], e[k]) {
			present = append(present, hexKey([]byte(k)))
		} else {
			if firstMissing == "" {
				firstMissing = k
			}
			missing = append(missing, hexKey([]byte(k)))
		}
	}
	if len(missing) == 0 {
		return nil
	}
	witness := ""
	if len(op.Witness) > 0 {
		witness = fmt.Sprintf(" (witness key %s)", hexKey(op.Witness))
	}
	return &Failure{
		Key: []byte(firstMissing), Observed: obs[firstMissing],
		Reason: fmt.Sprintf("batch atomicity violated: unACKed op %d is partly present%s: %d of %d keys show it, %d do not",
			op.ID, witness, len(present), len(e), len(missing)),
		Expected: fmt.Sprintf("all %d keys of op %d, or none of them", len(e), op.ID),
	}
}

// diagnose explains why an observed state is not allowed, as precisely as the oracle can.
func diagnose(obs, want KeyState, k string, ops []*Op, lastAcked map[string]*Op, base Baseline, stateOf stateOfFunc) string {
	switch {
	case obs.Kind == KeyAbsent && want.Kind != KeyAbsent:
		if op := lastAcked[k]; op != nil {
			return fmt.Sprintf("ACKed write LOST: op %d was acknowledged (ack_seq %d) but the key is absent", op.ID, op.AckSeq)
		}
		return fmt.Sprintf("durable data LOST: the key held %s after the last verified iteration and is now absent", want)
	case obs.Kind == KeyValue:
		if obs.OpID == want.OpID && want.Kind == KeyValue {
			return fmt.Sprintf("value has %d bytes but op %d wrote %d (truncated or extended value)", obs.Size, obs.OpID, want.Size)
		}
		for _, op := range ops {
			if op.ID != obs.OpID {
				continue
			}
			switch op.State {
			case StateRejected:
				return fmt.Sprintf("REJECTED op %d (ERR %s) appeared: it must never be applied", op.ID, op.Reason)
			case StateAcked:
				if last := lastAcked[k]; last != nil && last != op {
					if want.Kind == KeyAbsent {
						return fmt.Sprintf("ACKed DELETE undone (resurrection): op %d (ack_seq %d) deleted this key, but older op %d (ack_seq %d) is back", last.ID, last.AckSeq, op.ID, op.AckSeq)
					}
					return fmt.Sprintf("commit order violated: older ACKed op %d (ack_seq %d) won over later ACKed op %d (ack_seq %d)", op.ID, op.AckSeq, last.ID, last.AckSeq)
				}
				if want.Kind == KeyAbsent {
					return fmt.Sprintf("in-batch order violated (B5): the last op of ACKed op %d on this key is a DELETE, yet its value survived", op.ID)
				}
				return fmt.Sprintf("ACKed op %d is present but a different outcome was required", op.ID)
			case StateInFlight:
				return fmt.Sprintf("unACKed op %d is present on this key but its effect here does not match", op.ID)
			}
		}
		if b, inBase := base[k]; inBase && sameState(b, obs) {
			if last := lastAcked[k]; last != nil {
				if want.Kind == KeyAbsent {
					return fmt.Sprintf("ACKed DELETE undone (resurrection): op %d (ack_seq %d) deleted this key, but its previous value (op %d) is back", last.ID, last.AckSeq, obs.OpID)
				}
				return fmt.Sprintf("ACKed write undone: op %d (ack_seq %d) was acknowledged, but the key reverted to its previous value (op %d)", last.ID, last.AckSeq, obs.OpID)
			}
		}
		if st, ok := stateOf(obs.OpID); ok {
			if st == StateDead {
				return fmt.Sprintf("DEAD op %d resurfaced: it was superseded or lost in an earlier iteration and must never appear again", obs.OpID)
			}
			return fmt.Sprintf("op %d from an earlier iteration (state %s) is present but is not the verified baseline", obs.OpID, st)
		}
		return fmt.Sprintf("value names op %d, which this run never sent (foreign data)", obs.OpID)
	case obs.Kind == KeyEmpty:
		return "key holds an empty value that no allowed op wrote"
	}
	return "unexpected state"

}

// allowedList describes which ops could legally explain this key, with SENT and ACK times.
func allowedList(k string, want KeyState, ops []*Op, lastAcked map[string]*Op, inflightOn map[string][]*Op) []string {
	var out []string
	if op := lastAcked[k]; op != nil {
		out = append(out, fmt.Sprintf("op %d %s ACKED sent@%dns ack@%dns ack_seq=%d -> %s", op.ID, op.Kind, op.InvokeNS, op.AckNS, op.AckSeq, want))
	} else {
		out = append(out, fmt.Sprintf("baseline -> %s", want))
	}
	for _, op := range inflightOn[k] {
		out = append(out, fmt.Sprintf("op %d %s IN_FLIGHT sent@%dns -> %s", op.ID, op.Kind, op.InvokeNS, effects(op)[k]))
	}
	return out
}

// UpdateBaseline folds a passing iteration into the baseline: the observed state becomes truth.
func UpdateBaseline(base Baseline, res *CheckResult) {
	for k := range base {
		if _, ok := res.Observed[k]; !ok {
			delete(base, k)
		}
	}
	for k, st := range res.Observed {
		base[k] = st
	}
}
