package backend

import (
	"context"
	"encoding/json"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rnbguy/dbcop/mockserver/apalachebackend"
)

// NewApalacheStore uses an operator-managed Apalache v0.62.4 explorer, not the native oracle.
// Health is not version attestation; provisioning must verify the pinned artifact.
func NewApalacheStore(seed1, seed2 uint64, rpcURL string, timeout time.Duration) (*Store, error) {
	explorer, err := newApalacheExplorer(rpcURL, timeout)
	if err != nil {
		return nil, err
	}
	store, err := newStoreWithExplorer(seed1, seed2, explorer)
	if err != nil {
		return nil, errors.Join(err, explorer.Close())
	}
	return store, nil
}

type apalacheExplorer struct {
	client    *apalachebackend.Client
	timeout   time.Duration
	mu        sync.Mutex
	closed    bool
	active    sync.WaitGroup
	closeDone chan struct{}
	closeErr  error
}

func newApalacheExplorer(rpcURL string, timeout time.Duration) (*apalacheExplorer, error) {
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	client, err := apalachebackend.New(rpcURL, timeout)
	if err != nil {
		return nil, err
	}
	e := &apalacheExplorer{client: client, timeout: timeout, closeDone: make(chan struct{})}
	// Exercise the actual exported transition and ITF decoder before any SQL can publish data.
	result, err := e.Step(context.Background(), StepRequest{
		Kind: "read", Isolation: Serializable, Session: 0, Key: "capability",
		Candidates: []ReadSource{{Initial: true}},
	})
	if err == nil && (!result.Enabled || result.Source != (ReadSource{Initial: true})) {
		err = fmt.Errorf("Apalache capability transition did not return its sole root source")
	}
	if err != nil {
		return nil, errors.Join(fmt.Errorf("Apalache capability: %w", err), e.Close())
	}
	return e, nil
}

func (e *apalacheExplorer) Close() error {
	e.mu.Lock()
	if e.closed {
		done := e.closeDone
		e.mu.Unlock()
		<-done
		return e.closeErr
	}
	e.closed = true
	e.mu.Unlock()
	e.active.Wait()
	err := e.client.Close()
	e.mu.Lock()
	e.closeErr = err
	close(e.closeDone)
	e.mu.Unlock()
	return err
}

func (e *apalacheExplorer) Step(ctx context.Context, request StepRequest) (result StepResult, err error) {
	defer func() {
		reason := "enabled"
		if err != nil {
			reason = err.Error()
		} else if !result.Enabled {
			reason = "disabled"
		}
		slog.Debug("Apalache operation", "reason", reason, "operation", request.Kind,
			"session", request.Session, "source", result.Source.Version,
			"initial", result.Source.Initial, "local", result.Source.Local)
	}()
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return result, fmt.Errorf("Apalache explorer is closed")
	}
	e.active.Add(1)
	e.mu.Unlock()
	defer e.active.Done()
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, e.timeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if request.Kind == "read" && request.Latest {
		// This explicit override is deterministic; ordinary reads ask for one solver-selected source.
		sources := append([]ReadSource(nil), request.Candidates...)
		sort.SliceStable(sources, func(i, j int) bool {
			if sources[i].Local != sources[j].Local {
				return sources[i].Local
			}
			if sources[i].Initial != sources[j].Initial {
				return !sources[i].Initial
			}
			return sources[i].Version > sources[j].Version
		})
		if len(sources) == 0 {
			_, err = renderApalacheRequest(request)
			return result, err
		}
		for i, source := range sources {
			if source.Initial && (source.Local || source.Version != 0) || !source.Initial && source.Version == 0 {
				return result, fmt.Errorf("invalid explorer source identity")
			}
			if i > 0 && source == sources[i-1] {
				return result, fmt.Errorf("duplicate explorer source")
			}
		}
		for _, source := range sources {
			forced := request
			forced.Candidates = []ReadSource{source}
			result, err = e.successor(ctx, forced, nil)
			if err != nil || result.Enabled {
				return result, err
			}
		}
		return result, nil
	}
	return e.successor(ctx, request, nil)
}

type apalacheTransition struct {
	Index  *int     `json:"index"`
	Labels []string `json:"labels"`
}

type apalacheLoaded struct {
	Session    string `json:"sessionId"`
	Snapshot   *int   `json:"snapshotId"`
	Parameters struct {
		Init []apalacheTransition `json:"initTransitions"`
		Next []apalacheTransition `json:"nextTransitions"`
	} `json:"specParameters"`
}

type apalachePosition struct {
	Session    string `json:"sessionId"`
	Snapshot   *int   `json:"snapshotId"`
	Transition *int   `json:"transitionId"`
	Step       *int   `json:"newStepNo"`
	Status     string `json:"status"`
}

type apalacheSession struct {
	Session string `json:"sessionId"`
}

func validApalacheSession(session string) bool {
	if session == "" {
		return false
	}
	for _, c := range session {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return false
		}
	}
	return true
}

func apalacheTransitions(load apalacheLoaded) (int, int, error) {
	if !validApalacheSession(load.Session) || load.Snapshot == nil || *load.Snapshot < 0 {
		return 0, 0, fmt.Errorf("invalid Apalache load session/snapshot")
	}
	if len(load.Parameters.Init) != 1 || len(load.Parameters.Next) != 1 {
		return 0, 0, fmt.Errorf("Apalache requires one initializer and one TakeStep transition")
	}
	init, next := load.Parameters.Init[0], load.Parameters.Next[0]
	if init.Index == nil || *init.Index != 0 || next.Index == nil || *next.Index != 0 {
		return 0, 0, fmt.Errorf("invalid Apalache transition index")
	}
	labels := 0
	for _, label := range next.Labels {
		if label == "TakeStep" {
			labels++
		}
	}
	if labels != 1 {
		return 0, 0, fmt.Errorf("missing or ambiguous Apalache TakeStep label")
	}
	return *init.Index, *next.Index, nil
}

func (e *apalacheExplorer) solverTimeout(ctx context.Context) int {
	duration := e.timeout
	if deadline, ok := ctx.Deadline(); ok {
		duration = time.Until(deadline)
	}
	seconds := math.Ceil(duration.Seconds())
	if seconds < 1 {
		return 1
	}
	if seconds > math.MaxInt32 {
		return math.MaxInt32
	}
	return int(seconds)
}

func (e *apalacheExplorer) assume(ctx context.Context, session string, transition int) (bool, error) {
	var response apalachePosition
	err := e.client.Call(ctx, "assumeTransition", struct {
		Session      string `json:"sessionId"`
		Transition   int    `json:"transitionId"`
		CheckEnabled bool   `json:"checkEnabled"`
		Timeout      int    `json:"timeoutSec"`
	}{session, transition, true, e.solverTimeout(ctx)}, &response)
	if err != nil {
		return false, err
	}
	if response.Session != session || response.Snapshot == nil || *response.Snapshot < 0 ||
		response.Transition == nil || *response.Transition != transition {
		return false, fmt.Errorf("invalid Apalache assumption identity")
	}
	switch response.Status {
	case "ENABLED":
		return true, nil
	case "DISABLED":
		return false, nil
	default:
		return false, fmt.Errorf("Apalache assumption status %q is not a decision", response.Status)
	}
}

func (e *apalacheExplorer) advance(ctx context.Context, session string, step int) (int, error) {
	var response apalachePosition
	if err := e.client.Call(ctx, "nextStep", apalacheSession{session}, &response); err != nil {
		return 0, err
	}
	if response.Session != session || response.Snapshot == nil || *response.Snapshot < 0 || response.Step == nil || *response.Step != step {
		return 0, fmt.Errorf("invalid Apalache advance identity/step")
	}
	return *response.Snapshot, nil
}

// inspect is used only by real-service tests to enumerate observable models before disposal.
func (e *apalacheExplorer) successor(ctx context.Context, request StepRequest, inspect func(string, int, apalacheInput, StepResult) error) (result StepResult, err error) {
	input, err := renderApalacheRequest(request)
	if err != nil {
		return result, err
	}
	var load apalacheLoaded
	loadErr := e.client.Call(ctx, "loadSpec", struct {
		Sources []string `json:"sources"`
		Format  string   `json:"format"`
		Init    string   `json:"init"`
		Next    string   `json:"next"`
		Exports []string `json:"exports"`
	}{apalachebackend.Sources(input.module), "tla", "Init", "Next", []string{"Result"}}, &load)
	// Even malformed load metadata may contain a real allocated session; dispose it before returning.
	if load.Session != "" {
		defer func() {
			cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			var disposed apalacheSession
			cleanupErr := e.client.Call(cleanup, "disposeSpec", apalacheSession{load.Session}, &disposed)
			if cleanupErr == nil && disposed.Session != load.Session {
				cleanupErr = fmt.Errorf("invalid Apalache disposal session")
			}
			if cleanupErr != nil {
				err = errors.Join(err, fmt.Errorf("dispose Apalache session %s: %w", load.Session, cleanupErr))
				result = StepResult{}
			}
			if contextErr := ctx.Err(); contextErr != nil {
				err = errors.Join(err, contextErr)
				result = StepResult{}
			}
		}()
	}
	if loadErr != nil {
		// A lost response can allocate an unknown session; the API has no recovery/list operation.
		return result, fmt.Errorf("Apalache loadSpec failed (a lost response may leave an unknown remote session): %w", loadErr)
	}
	init, next, err := apalacheTransitions(load)
	if err != nil {
		return result, err
	}
	enabled, err := e.assume(ctx, load.Session, init)
	if err != nil {
		return result, err
	}
	if !enabled {
		return result, fmt.Errorf("Apalache initializer is disabled")
	}
	if _, err = e.advance(ctx, load.Session, 1); err != nil {
		return result, err
	}
	enabled, err = e.assume(ctx, load.Session, next)
	if err != nil || !enabled {
		return result, err
	}
	snapshot, err := e.advance(ctx, load.Session, 2)
	if err != nil {
		return result, err
	}
	var query struct {
		Session string          `json:"sessionId"`
		Value   json.RawMessage `json:"operatorValue"`
	}
	if err = e.client.Call(ctx, "query", struct {
		Session  string   `json:"sessionId"`
		Kinds    []string `json:"kinds"`
		Operator string   `json:"operator"`
		Timeout  int      `json:"timeoutSec"`
	}{load.Session, []string{"OPERATOR"}, "Result", e.solverTimeout(ctx)}, &query); err != nil {
		return result, err
	}
	if query.Session != load.Session {
		return result, fmt.Errorf("invalid Apalache query session")
	}
	result, err = input.decode(query.Value)
	if err != nil {
		return StepResult{}, err
	}
	if inspect != nil {
		if err = inspect(load.Session, snapshot, input, result); err != nil {
			return StepResult{}, err
		}
	}
	return result, nil
}

type apalacheEvent struct {
	kind, key int
	initial   bool
	version   uint64
}
type apalacheTxn struct {
	session, height int
	events          []apalacheEvent
}
type apalacheInput struct {
	module     string
	kind       int
	candidates map[ReadSource]ReadSource
}

type apalacheBigInt uint64

func (n *apalacheBigInt) UnmarshalJSONFrom(decoder *jsontext.Decoder) error {
	var integer struct {
		Text string `json:"#bigint"`
	}
	if err := jsonv2.UnmarshalDecode(decoder, &integer, jsonv2.RejectUnknownMembers(true)); err != nil {
		return fmt.Errorf("expected ITF #bigint string: %w", err)
	}
	text := integer.Text
	if text == "" {
		return fmt.Errorf("empty ITF integer")
	}
	for _, c := range text {
		if c < '0' || c > '9' {
			return fmt.Errorf("invalid nonnegative ITF integer %q", text)
		}
	}
	value, err := strconv.ParseUint(text, 10, 64)
	if err != nil {
		return fmt.Errorf("ITF integer overflow: %w", err)
	}
	*n = apalacheBigInt(value)
	return nil
}

func apalacheInteger(raw json.RawMessage) (uint64, error) {
	var integer apalacheBigInt
	err := jsonv2.Unmarshal(raw, &integer)
	return uint64(integer), err
}

func (input apalacheInput) decode(raw json.RawMessage) (StepResult, error) {
	var record struct {
		Kind    *apalacheBigInt `json:"kind"`
		Version *apalacheBigInt `json:"version"`
		Initial *bool           `json:"initial"`
		Local   *bool           `json:"local"`
	}
	if err := jsonv2.Unmarshal(raw, &record, jsonv2.RejectUnknownMembers(true)); err != nil {
		return StepResult{}, fmt.Errorf("invalid Apalache Result record: %w", err)
	}
	if record.Kind == nil || uint64(*record.Kind) != uint64(input.kind) {
		return StepResult{}, fmt.Errorf("invalid Apalache Result operation")
	}
	if record.Version == nil || record.Initial == nil || record.Local == nil {
		return StepResult{}, fmt.Errorf("missing or null Apalache Result source field")
	}
	source := ReadSource{Version: Version(*record.Version), Initial: *record.Initial, Local: *record.Local}
	if input.kind != 0 {
		if source != (ReadSource{}) {
			return StepResult{}, fmt.Errorf("non-read Apalache result contains a source")
		}
		return StepResult{Enabled: true}, nil
	}
	original, ok := input.candidates[source]
	if !ok {
		return StepResult{}, fmt.Errorf("Apalache selected source outside request universe")
	}
	return StepResult{Enabled: true, Source: original}, nil
}

func renderApalacheRequest(request StepRequest) (apalacheInput, error) {
	input := apalacheInput{candidates: make(map[ReadSource]ReadSource)}
	switch request.Kind {
	case "read":
		input.kind = 0
	case "write":
		input.kind = 1
	case "commit":
		input.kind = 2
	default:
		return input, fmt.Errorf("unknown explorer operation %q", request.Kind)
	}
	if request.Isolation < ReadCommitted || request.Isolation > Serializable || request.Session < 0 {
		return input, fmt.Errorf("invalid explorer isolation/session")
	}
	keys := make(map[string]int)
	keyID := func(key string) int {
		if n, ok := keys[key]; ok {
			return n
		}
		n := len(keys) + 1
		keys[key] = n
		return n
	}
	versions := make(map[Version]uint64)
	versionID := func(version Version) uint64 {
		if version == 0 {
			return 0
		}
		if n, ok := versions[version]; ok {
			return n
		}
		n := uint64(len(versions)) + 1
		versions[version] = n
		return n
	}
	sessions, heights := make(map[int]int), make(map[int]int)
	sessionID := func(session int) int {
		if n, ok := sessions[session]; ok {
			return n
		}
		n := len(sessions) + 1
		sessions[session] = n
		return n
	}
	convert := func(txn LoggedTxn) (apalacheTxn, error) {
		if txn.Session < 0 {
			return apalacheTxn{}, fmt.Errorf("negative committed session")
		}
		result := apalacheTxn{session: sessionID(txn.Session), height: heights[txn.Session]}
		heights[txn.Session]++
		addRead := func(read LoggedRead) {
			result.events = append(result.events, apalacheEvent{kind: 0, key: keyID(read.Key), initial: read.Initial, version: versionID(read.WriteID)})
		}
		addWrite := func(write CommittedWrite) {
			result.events = append(result.events, apalacheEvent{kind: 1, key: keyID(write.Key), version: versionID(write.ID)})
		}
		if len(txn.Events) != 0 {
			for _, event := range txn.Events {
				if (event.Read == nil) == (event.Write == nil) {
					return result, fmt.Errorf("committed event must have exactly one operation")
				}
				if event.Read != nil {
					addRead(*event.Read)
				} else {
					addWrite(*event.Write)
				}
			}
		} else {
			// Match the existing legacy log convention; new commits always carry Events.
			for _, write := range txn.Writes {
				addWrite(write)
			}
			for _, read := range txn.Reads {
				addRead(read)
			}
		}
		return result, nil
	}
	committed := make([]apalacheTxn, 0, len(request.Committed))
	for _, txn := range request.Committed {
		converted, err := convert(txn)
		if err != nil {
			return input, err
		}
		committed = append(committed, converted)
	}
	liveSession, liveHeight := sessionID(request.Session), heights[request.Session]
	var live, reads []apalacheEvent
	writes := uint64(0)
	for _, event := range request.Live {
		if event.IsRead {
			read := apalacheEvent{kind: 0, key: keyID(event.Read.Key), initial: event.Read.Initial, version: versionID(event.Read.WriteID)}
			live = append(live, read)
			reads = append(reads, read)
		} else {
			writes++
			live = append(live, apalacheEvent{kind: 1, key: keyID(cellKey(event.Write.Tab, event.Write.Pkey, event.Write.Col)), version: writes})
		}
	}
	proposed := apalacheTxn{session: liveSession, height: liveHeight}
	if request.Kind == "commit" {
		if request.Transaction.Session != request.Session {
			return input, fmt.Errorf("commit session differs from request")
		}
		var err error
		proposed, err = convert(request.Transaction)
		if err != nil {
			return input, err
		}
	}
	var candidates []ReadSource
	for _, source := range request.Candidates {
		if source.Initial && (source.Local || source.Version != 0) || !source.Initial && source.Version == 0 {
			return input, fmt.Errorf("invalid explorer source identity")
		}
		normalized := source
		if !source.Local {
			normalized.Version = Version(versionID(source.Version))
		}
		if _, duplicate := input.candidates[normalized]; duplicate {
			return input, fmt.Errorf("duplicate explorer source")
		}
		input.candidates[normalized] = source
		candidates = append(candidates, normalized)
	}
	if request.Kind == "read" && request.Key == "" {
		return input, fmt.Errorf("read requires a semantic cell key")
	}
	readKey := keyID(request.Key)
	writeKey := keyID(cellKey(request.Write.Tab, request.Write.Pkey, request.Write.Col))
	if request.Kind == "write" && request.Key != cellKey(request.Write.Tab, request.Write.Pkey, request.Write.Col) {
		return input, fmt.Errorf("write key differs from staged cell")
	}
	var b strings.Builder
	b.WriteString("---- MODULE RequestData ----\nEXTENDS Isolation\n")
	fmt.Fprintf(&b, "RequestKind == %d\nLevel == %d\nLiveSession == %d\nLiveHeight == %d\nReadKey == %d\n", input.kind, request.Isolation, liveSession, liveHeight, readKey)
	b.WriteString("\\* @type: $history;\nCommitted == ")
	b.WriteString(apalacheHistoryLiteral(committed))
	b.WriteString("\n\\* @type: Seq({kind: Int, key: Int, initial: Bool, version: Int});\nLive == ")
	b.WriteString(apalacheEventsLiteral(live))
	fmt.Fprintf(&b, "\n\\* @type: {kind: Int, key: Int, initial: Bool, version: Int};\nProposedWrite == [kind |-> 1, key |-> %d, initial |-> FALSE, version |-> %d]\n", writeKey, writes+1)
	b.WriteString("\\* @type: Seq({version: Int, initial: Bool, local: Bool});\nCandidates == <<")
	for i, source := range candidates {
		if i != 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, "[version |-> %d, initial |-> %s, local |-> %s]", source.Version, tlaBool(source.Initial), tlaBool(source.Local))
	}
	b.WriteString(">>\n")
	// Keep the raw history literal: symbolic observed' makes every event lookup opaque.
	b.WriteString("\\* @type: ({version: Int, initial: Bool, local: Bool}) => $history;\nOrderHistory(s) == <<")
	for i, txn := range committed {
		if i != 0 {
			b.WriteByte(',')
		}
		b.WriteString(apalacheTxnLiteral(txn))
	}
	if request.Kind == "read" || request.Kind == "commit" {
		if len(committed) != 0 {
			b.WriteByte(',')
		}
		if request.Kind == "commit" {
			b.WriteString(apalacheTxnLiteral(proposed))
		} else {
			fmt.Fprintf(&b, "[session |-> %d, height |-> %d, committed |-> TRUE, events |-> ", liveSession, liveHeight)
			events := apalacheEventsLiteral(reads)
			b.WriteString(events[:len(events)-2])
			if len(reads) != 0 {
				b.WriteByte(',')
			}
			fmt.Fprintf(&b, "[kind |-> 0, key |-> %d, initial |-> s.initial, version |-> s.version]>>]", readKey)
		}
	}
	// Exact raw write sites avoid symbolic write slots for every historical read.
	b.WriteString(">>\n\\* @type: Seq({txn: Int, position: Int, event: {kind: Int, key: Int, initial: Bool, version: Int}});\nProjectedWrites == <<")
	firstWrite := true
	projectWrites := func(txn int, events []apalacheEvent) {
		for position, event := range events {
			if event.kind != 1 {
				continue
			}
			if !firstWrite {
				b.WriteByte(',')
			}
			firstWrite = false
			fmt.Fprintf(&b, "[txn |-> %d, position |-> %d, event |-> [kind |-> %d, key |-> %d, initial |-> %s, version |-> %d]]",
				txn, position+1, event.kind, event.key, tlaBool(event.initial), event.version)
		}
	}
	for i, txn := range committed {
		projectWrites(i+1, txn.events)
	}
	if request.Kind == "commit" {
		projectWrites(len(committed)+1, proposed.events)
	}
	b.WriteString(">>\n")
	if request.Kind != "write" {
		// Materialize read/write facts and dependencies once; premises otherwise inline every raw lookup.
		names := []string{"orderEdges"}
		if request.Isolation == Causal {
			for i := 0; i <= len(committed)+1; i++ {
				names = append(names, fmt.Sprintf("causalReach%d", i))
			}
		}
		b.WriteString("VARIABLES\n\\* @type: Set({txn: Int, position: Int, key: Int, source: Int});\norderReads,\n\\* @type: Set(<<Int, Int>>);\norderWriters,\n")
		for i, name := range names {
			if i != 0 {
				b.WriteString(",\n")
			}
			fmt.Fprintf(&b, "\\* @type: Set(<<Int, Int>>);\n%s", name)
		}
		b.WriteString("\nClosureInit ==\n/\\ orderReads = {}\n/\\ orderWriters = {}\n")
		for _, name := range names {
			fmt.Fprintf(&b, "/\\ %s = {}\n", name)
		}
		b.WriteString("\\* @type: ($history, Set(<<Int, Int>>)) => Bool;\nClosureStep(h, ws) ==\n/\\ orderReads' = ReadFacts(h, ws)\n/\\ orderWriters' = WriterFacts(h, ws)\n/\\ orderEdges' = EdgesFromFacts(h, orderReads')\n")
		for i, name := range names[1:] {
			fmt.Fprintf(&b, "/\\ %s' = ReachStep(Ids(h), %s', %d)\n", name, names[i], i)
		}
		b.WriteString("ClosureReads == orderReads'\nClosureWriters == orderWriters'\nClosureEdges == orderEdges'\n")
		if request.Isolation == Causal {
			fmt.Fprintf(&b, "ClosureReach == %s'\n", names[len(names)-1])
		} else {
			b.WriteString("\\* @type: Set(<<Int, Int>>);\nClosureReach == {}\n")
		}
	} else {
		b.WriteString("ClosureInit == TRUE\n\\* @type: ($history, Set(<<Int, Int>>)) => Bool;\nClosureStep(h, ws) == TRUE\n\\* @type: Set({txn: Int, position: Int, key: Int, source: Int});\nClosureReads == {}\n\\* @type: Set(<<Int, Int>>);\nClosureWriters == {}\n\\* @type: Set(<<Int, Int>>);\nClosureEdges == {}\n\\* @type: Set(<<Int, Int>>);\nClosureReach == {}\n")
	}
	b.WriteString("====\n")
	input.module = b.String()
	return input, nil
}

func tlaBool(value bool) string {
	if value {
		return "TRUE"
	}
	return "FALSE"
}
func apalacheEventsLiteral(events []apalacheEvent) string {
	var b strings.Builder
	b.WriteString("<<")
	for i, event := range events {
		if i != 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, "[kind |-> %d, key |-> %d, initial |-> %s, version |-> %d]", event.kind, event.key, tlaBool(event.initial), event.version)
	}
	b.WriteString(">>")
	return b.String()
}
func apalacheTxnLiteral(txn apalacheTxn) string {
	return fmt.Sprintf("[session |-> %d, height |-> %d, committed |-> TRUE, events |-> %s]", txn.session, txn.height, apalacheEventsLiteral(txn.events))
}
func apalacheHistoryLiteral(history []apalacheTxn) string {
	var b strings.Builder
	b.WriteString("<<")
	for i, txn := range history {
		if i != 0 {
			b.WriteByte(',')
		}
		b.WriteString(apalacheTxnLiteral(txn))
	}
	b.WriteString(">>")
	return b.String()
}
