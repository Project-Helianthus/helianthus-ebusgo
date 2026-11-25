package localworkerhost

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestOracleLocalWorkerHost records the pin's supervisor plan, environment,
// report and lifecycle. The stimuli are the pin's own fixtures.
func TestOracleLocalWorkerHost(t *testing.T) {
	oracleRecordChildEnv(t)
	oracleRecordValidate(t)
	oracleRecordNewConfig(t)
	oracleRecordArgv(t)
	oracleRecordReport(t)
	oracleRecordLifecycle(t)
}

func oracleRecordChildEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{"GOGC", "GOMEMLIMIT", "GODEBUG", "GOMAXPROCS"} {
		env := ChildEnv([]string{"PATH=/usr/bin", key + "=parent-value", "LANG=C"}, aa27Policy)
		oracleWrite(t, "child-env-"+key+".json", oracleCase{
			Component: "localworkerhost",
			Source:    "internal/localworkerhost/supervisor.go",
			Stimulus:  "TestAA27ChildEnvironmentNeverInheritsGatewayRuntimeSettings/" + key,
			Input: oracleInput{
				Qualifiers: map[string]any{"stripped_key": key, "policy_gc": aa27Policy.GC, "policy_memory_limit": aa27Policy.MemoryLimit},
				State:      map[string]any{"base": []any{"PATH=/usr/bin", key + "=parent-value", "LANG=C"}},
			},
			Output: oracleOutput{Decisions: []any{map[string]any{"env": oracleStrings(env)}}},
		})
	}
}

func oracleRecordValidate(t *testing.T) {
	t.Helper()
	validEnv := map[string]string{"GOGC": "off", "GOMEMLIMIT": "64MiB"}
	positive := ValidateChild(aa27ValidPlan(), aa27Self, aa27ValidObserved(), aa27Env(validEnv))
	oracleWrite(t, "validate-positive.json", oracleCase{
		Component: "localworkerhost",
		Source:    "internal/localworkerhost/supervisor.go",
		Stimulus:  "TestAA27ValidateChildVariesEveryIdentityDescriptorAndPolicyField/positive",
		Input:     oracleInput{Qualifiers: map[string]any{"mutation": "none"}, State: oracleValidateState(aa27ValidPlan(), aa27Self, aa27ValidObserved(), validEnv)},
		Output:    oracleOutput{Decisions: []any{map[string]any{"error": oracleErr(positive)}}},
	})
	cases := []struct {
		name   string
		mutate func(*ChildPlan, *Identity, []Observed, map[string]string)
	}{
		{"plan-build", func(p *ChildPlan, _ *Identity, _ []Observed, _ map[string]string) {
			p.Identity.Build = DigestOf([]byte("aa27-build-b"))
		}},
		{"plan-executable", func(p *ChildPlan, _ *Identity, _ []Observed, _ map[string]string) {
			p.Identity.Executable = DigestOf([]byte("aa27-image-b"))
		}},
		{"plan-abi", func(p *ChildPlan, _ *Identity, _ []Observed, _ map[string]string) { p.Identity.ABI = ABIVersion + 1 }},
		{"child-abi", func(_ *ChildPlan, s *Identity, _ []Observed, _ map[string]string) { s.ABI = ABIVersion + 1 }},
		{"child-build", func(_ *ChildPlan, s *Identity, _ []Observed, _ map[string]string) {
			s.Build = DigestOf([]byte("aa27-build-c"))
		}},
		{"child-executable", func(_ *ChildPlan, s *Identity, _ []Observed, _ map[string]string) {
			s.Executable = DigestOf([]byte("aa27-image-replaced"))
		}},
		{"zero-worker", func(p *ChildPlan, _ *Identity, _ []Observed, _ map[string]string) { p.Generation.Worker = 0 }},
		{"zero-stream", func(p *ChildPlan, _ *Identity, _ []Observed, _ map[string]string) { p.Generation.Stream = 0 }},
		{"zero-registration", func(p *ChildPlan, _ *Identity, _ []Observed, _ map[string]string) { p.Generation.Registration = 0 }},
		{"zero-token", func(p *ChildPlan, _ *Identity, _ []Observed, _ map[string]string) { p.Generation.Token = 0 }},
		{"control-role", func(p *ChildPlan, _ *Identity, _ []Observed, _ map[string]string) {
			p.Descriptors[0].Role = RoleMapping
		}},
		{"control-size", func(p *ChildPlan, _ *Identity, _ []Observed, _ map[string]string) {
			p.Descriptors[0].Size = ReportBytes + 8
		}},
		{"mapping-role", func(p *ChildPlan, _ *Identity, _ []Observed, _ map[string]string) {
			p.Descriptors[1].Role = RoleControl
		}},
		{"mapping-size-unaligned", func(p *ChildPlan, _ *Identity, _ []Observed, _ map[string]string) { p.Descriptors[1].Size = 4100 }},
		{"mapping-size-above-bound", func(p *ChildPlan, _ *Identity, _ []Observed, _ map[string]string) {
			p.Descriptors[1].Size = MaxMappingBytes + 8
		}},
		{"control-fd", func(_ *ChildPlan, _ *Identity, o []Observed, _ map[string]string) { o[0].FD = 5 }},
		{"control-kind", func(_ *ChildPlan, _ *Identity, o []Observed, _ map[string]string) { o[0].Kind = KindMapping }},
		{"mapping-kind", func(_ *ChildPlan, _ *Identity, o []Observed, _ map[string]string) { o[1].Kind = KindStream }},
		{"mapping-observed-size", func(_ *ChildPlan, _ *Identity, o []Observed, _ map[string]string) { o[1].Size = 8192 }},
		{"inherited-gogc", func(_ *ChildPlan, _ *Identity, _ []Observed, e map[string]string) { e["GOGC"] = "50" }},
		{"inherited-gomemlimit", func(_ *ChildPlan, _ *Identity, _ []Observed, e map[string]string) { e["GOMEMLIMIT"] = "2GiB" }},
	}
	for _, tc := range cases {
		plan, self, observed := aa27ValidPlan(), aa27Self, aa27ValidObserved()
		env := map[string]string{"GOGC": "off", "GOMEMLIMIT": "64MiB"}
		tc.mutate(&plan, &self, observed, env)
		err := ValidateChild(plan, self, observed, aa27Env(env))
		oracleWrite(t, "validate-"+tc.name+".json", oracleCase{
			Component: "localworkerhost",
			Source:    "internal/localworkerhost/supervisor.go",
			Stimulus:  "TestAA27ValidateChildVariesEveryIdentityDescriptorAndPolicyField/" + tc.name,
			Input:     oracleInput{Qualifiers: map[string]any{"mutation": tc.name}, State: oracleValidateState(plan, self, observed, env)},
			Output:    oracleOutput{Decisions: []any{map[string]any{"error": oracleErr(err)}}},
		})
	}
	shortObserved := aa27ValidObserved()[:1]
	short := ValidateChild(aa27ValidPlan(), aa27Self, shortObserved, aa27Env(validEnv))
	oracleWrite(t, "validate-missing-descriptor.json", oracleCase{
		Component: "localworkerhost",
		Source:    "internal/localworkerhost/supervisor.go",
		Stimulus:  "TestAA27ValidateChildVariesEveryIdentityDescriptorAndPolicyField/missing-descriptor",
		Input:     oracleInput{Qualifiers: map[string]any{"mutation": "drop-observed-mapping"}, State: oracleValidateState(aa27ValidPlan(), aa27Self, shortObserved, validEnv)},
		Output:    oracleOutput{Decisions: []any{map[string]any{"error": oracleErr(short)}}},
	})
}

func oracleRecordNewConfig(t *testing.T) {
	t.Helper()
	cases := []struct {
		name   string
		mutate func(*Config)
	}{
		{"relative-executable", func(c *Config) { c.Executable = "gateway" }},
		{"zero-build", func(c *Config) { c.Self.Build = Digest{} }},
		{"zero-executable", func(c *Config) { c.Self.Executable = Digest{} }},
		{"wrong-abi", func(c *Config) { c.Self.ABI = ABIVersion + 1 }},
		{"layout-order", func(c *Config) { c.Descriptors = []Descriptor{aa27Layout[1], aa27Layout[0]} }},
		{"empty-gc", func(c *Config) { c.Runtime.GC = "" }},
		{"empty-memory-limit", func(c *Config) { c.Runtime.MemoryLimit = "" }},
		{"lease-ttl", func(c *Config) { c.LeaseTTL = c.LeaseInterval }},
		{"kill-grace", func(c *Config) { c.KillGrace = 0 }},
		{"allocator", func(c *Config) { c.Allocator = nil }},
		{"launcher", func(c *Config) { c.Launcher = nil }},
		{"clock", func(c *Config) { c.Clock = nil }},
	}
	for _, tc := range cases {
		cfg := aa27Config(newAA27Clock(), &aa27Launcher{}, &aa27Allocator{})
		tc.mutate(&cfg)
		_, err := New(cfg)
		oracleWrite(t, "new-config-"+tc.name+".json", oracleCase{
			Component: "localworkerhost",
			Source:    "internal/localworkerhost/supervisor.go",
			Stimulus:  "TestAA27NewRejectsInvalidConfiguration/" + tc.name,
			Input:     oracleInput{Qualifiers: map[string]any{"mutation": tc.name}, Clock: map[string]any{"now": time.Unix(1000, 0).UTC().Format(time.RFC3339Nano)}, State: map[string]any{"config": oracleConfigState(cfg)}},
			Output:    oracleOutput{Decisions: []any{map[string]any{"error": oracleErr(err), "is_config": errors.Is(err, ErrConfig)}}},
		})
	}
}

func oracleRecordArgv(t *testing.T) {
	t.Helper()
	plan := aa27ValidPlan()
	argv := plan.Argv("/opt/gateway/bin/gateway")
	parsed, err := ParseChildArgv(argv[1:])
	oracleWrite(t, "argv-round-trip.json", oracleCase{
		Component: "localworkerhost",
		Source:    "internal/localworkerhost/supervisor.go",
		Stimulus:  "TestAA27ParseChildArgvAcceptsOnlyTheExactPlan/positive",
		Input:     oracleInput{Qualifiers: map[string]any{"executable": "/opt/gateway/bin/gateway"}, State: withArgs(oraclePlanState(plan), argv[1:])},
		Output: oracleOutput{Decisions: []any{map[string]any{
			"error":             oracleErr(err),
			"argv":              oracleStrings(argv),
			"identity_matches":  parsed.Identity == plan.Identity,
			"generation":        oracleGeneration(parsed.Generation),
			"runtime_gc":        parsed.Runtime.GC,
			"runtime_memory":    parsed.Runtime.MemoryLimit,
			"lease_ms":          parsed.LeaseInterval.Milliseconds(),
			"descriptor_layout": oracleLayout(parsed.Descriptors),
			"build_hex":         hex.EncodeToString(parsed.Identity.Build[:]),
			"executable_hex":    hex.EncodeToString(parsed.Identity.Executable[:]),
		}}},
	})
	args := argv[1:]
	negatives := []struct {
		name string
		args []string
	}{
		{"flag-alone", []string{ChildFlag}},
		{"trailing-gateway-flag", append(append([]string(nil), args...), "-matter-library-enabled")},
		{"missing-field", append([]string(nil), args[:len(args)-1]...)},
		{"wrong-selector", append([]string{"--internal-other"}, args[1:]...)},
	}
	swapped := append([]string(nil), args...)
	swapped[4], swapped[5] = swapped[5], swapped[4]
	negatives = append(negatives, struct {
		name string
		args []string
	}{"reordered-fields", swapped})
	for i, key := range childArgvKeys {
		bad := append([]string(nil), args...)
		bad[i+1] = key + "="
		negatives = append(negatives, struct {
			name string
			args []string
		}{"empty-" + key, bad})
	}
	for _, tc := range negatives {
		_, parseErr := ParseChildArgv(tc.args)
		oracleWrite(t, "argv-"+tc.name+".json", oracleCase{
			Component: "localworkerhost",
			Source:    "internal/localworkerhost/supervisor.go",
			Stimulus:  "TestAA27ParseChildArgvAcceptsOnlyTheExactPlan/" + tc.name,
			Input:     oracleInput{Qualifiers: map[string]any{"mutation": tc.name}, State: withArgs(map[string]any{"argc": len(tc.args)}, tc.args)},
			Output:    oracleOutput{Decisions: []any{map[string]any{"error": oracleErr(parseErr)}}},
		})
	}
}

func oracleRecordReport(t *testing.T) {
	t.Helper()
	report := Report{
		Kind:        ReportReady,
		Identity:    aa27Self,
		Generation:  Generation{Worker: 3, Stream: 5, Registration: 7, Token: 11},
		Descriptors: aa27Layout,
		State:       WorkerPassive,
	}
	raw, err := EncodeReport(report)
	decoded, decodeErr := DecodeReport(raw[:])
	oracleWrite(t, "report-round-trip.json", oracleCase{
		Component: "localworkerhost",
		Source:    "internal/localworkerhost/supervisor.go",
		Stimulus:  "TestAA27ReportEncodingRoundTripsAndRefusesCorruption/positive",
		Input: oracleInput{
			Bytes:      hex.EncodeToString(raw[:]),
			Qualifiers: map[string]any{"kind": "ready", "tx_active": false, "decoder_operand": "bytes", "decoder_operand_length": len(raw)},
			State: map[string]any{
				"generation": oracleGeneration(report.Generation), "layout": oracleLayout(report.Descriptors),
				"identity_build_hex": hex.EncodeToString(report.Identity.Build[:]), "identity_executable_hex": hex.EncodeToString(report.Identity.Executable[:]),
				"worker_state": report.State,
			},
		},
		Output: oracleOutput{Decisions: []any{map[string]any{
			"encode_error":     oracleErr(err),
			"decode_error":     oracleErr(decodeErr),
			"identity_matches": decoded.Identity == report.Identity,
			"generation":       oracleGeneration(decoded.Generation),
			"layout":           oracleLayout(decoded.Descriptors),
			"state":            decoded.State,
			"tx_active":        decoded.TXActive,
			"kind":             decoded.Kind,
			"build_hex":        hex.EncodeToString(decoded.Identity.Build[:]),
			"executable_hex":   hex.EncodeToString(decoded.Identity.Executable[:]),
		}}},
	})
	for _, tc := range []struct {
		name   string
		offset int
	}{
		{"magic", 0},
		{"tx-flag", 6},
		{"descriptor-count", 7},
		{"reserved", 12},
		{"descriptor-padding", 121},
	} {
		bad := raw
		bad[tc.offset] ^= 0x7f
		_, corruptErr := DecodeReport(bad[:])
		oracleWrite(t, "report-corrupt-"+tc.name+".json", oracleCase{
			Component: "localworkerhost",
			Source:    "internal/localworkerhost/supervisor.go",
			Stimulus:  "TestAA27ReportEncodingRoundTripsAndRefusesCorruption/" + tc.name,
			Input: oracleInput{
				Bytes:      hex.EncodeToString(bad[:]),
				Qualifiers: map[string]any{"offset": tc.offset, "decoder_operand": "bytes", "decoder_operand_length": len(bad)},
				State:      map[string]any{"xor": 127, "encoded_original_hex": hex.EncodeToString(raw[:])},
			},
			Output: oracleOutput{Decisions: []any{map[string]any{"error": oracleErr(corruptErr), "is_report": errors.Is(corruptErr, ErrReport)}}},
		})
	}
	_, shortErr := DecodeReport(raw[:ReportBytes-1])
	oracleWrite(t, "report-short.json", oracleCase{
		Component: "localworkerhost",
		Source:    "internal/localworkerhost/supervisor.go",
		Stimulus:  "TestAA27ReportEncodingRoundTripsAndRefusesCorruption/short",
		Input: oracleInput{
			Bytes:      hex.EncodeToString(raw[:ReportBytes-1]),
			Qualifiers: map[string]any{"length": ReportBytes - 1, "decoder_operand": "bytes", "decoder_operand_length": ReportBytes - 1},
			State:      map[string]any{"encoded_original_hex": hex.EncodeToString(raw[:])},
		},
		Output: oracleOutput{Decisions: []any{map[string]any{"error": oracleErr(shortErr), "is_report": errors.Is(shortErr, ErrReport)}}},
	})
}

func oracleRecordLifecycle(t *testing.T) {
	t.Helper()
	fixture := aa27StartReady(t)
	spec, _ := fixture.launcher.last()
	plan, err := ParseChildArgv(spec.Args[1:])
	closed := make([]any, 0)
	if stopErr := fixture.sup.Stop(context.Background()); stopErr != nil {
		t.Fatal(stopErr)
	}
	for _, resource := range fixture.allocator.all() {
		closed = append(closed, map[string]any{"role": resource.Role(), "size": resource.Size(), "closes": resource.closed()})
	}
	status := fixture.sup.Status()
	oracleWrite(t, "lifecycle-start-ready-stop.json", oracleCase{
		Component: "localworkerhost",
		Source:    "internal/localworkerhost/supervisor.go",
		Stimulus:  "TestAA27StartLaunchesExplicitArgvEnvironmentAndFixedDescriptors",
		Input: oracleInput{
			Qualifiers: map[string]any{"executable": "/opt/gateway/bin/gateway"},
			Clock:      map[string]any{"now": time.Unix(1000, 0).UTC().Format(time.RFC3339Nano)},
			State:      map[string]any{"lease_ms": int64(100)},
		},
		Output: oracleOutput{
			Decisions: []any{map[string]any{
				"parse_error":      oracleErr(err),
				"path":             spec.Path,
				"argv":             oracleStrings(spec.Args),
				"env":              oracleStrings(spec.Env),
				"identity_matches": plan.Identity == aa27Self,
				"generation":       oracleGeneration(plan.Generation),
				"spec_generation":  oracleGeneration(spec.Generation),
				"runtime_gc":       plan.Runtime.GC,
				"runtime_memory":   plan.Runtime.MemoryLimit,
				"lease_ms":         plan.LeaseInterval.Milliseconds(),
				"files":            oracleResources(spec.Files),
				"build_hex":        hex.EncodeToString(plan.Identity.Build[:]),
				"executable_hex":   hex.EncodeToString(plan.Identity.Executable[:]),
			}},
			Counters: map[string]any{"state_after_stop": string(status.State), "closed": closed},
		},
	})
}

// oracleValidateState lists every operand ValidateChild receives: the parsed
// plan, the child's own identity, the observed inherited descriptors and the
// environment the child reads its runtime policy from.
func oracleValidateState(plan ChildPlan, self Identity, observed []Observed, env map[string]string) map[string]any {
	state := oraclePlanState(plan)
	state["plan_build_hex"] = hex.EncodeToString(plan.Identity.Build[:])
	state["plan_executable_hex"] = hex.EncodeToString(plan.Identity.Executable[:])
	state["self"] = map[string]any{
		"abi": self.ABI, "build_hex": hex.EncodeToString(self.Build[:]), "executable_hex": hex.EncodeToString(self.Executable[:]),
	}
	descriptors := make([]any, 0, len(observed))
	for _, item := range observed {
		descriptors = append(descriptors, map[string]any{"fd": item.FD, "kind": item.Kind, "size": item.Size})
	}
	state["observed"] = descriptors
	variables := make(map[string]any, len(env))
	for key, value := range env {
		variables[key] = value
	}
	state["environment"] = variables
	return state
}

// withArgs adds the argument vector given to ParseChildArgv.
func withArgs(state map[string]any, args []string) map[string]any {
	state["args"] = oracleStrings(args)
	return state
}

// oracleConfigState lists every value of the Config handed to New, after the
// case's mutation; the allocator, launcher and clock are named by presence.
func oracleConfigState(cfg Config) map[string]any {
	return map[string]any{
		"self": map[string]any{
			"abi": cfg.Self.ABI, "build_hex": hex.EncodeToString(cfg.Self.Build[:]), "executable_hex": hex.EncodeToString(cfg.Self.Executable[:]),
		},
		"executable":        cfg.Executable,
		"process_args":      oracleStrings(cfg.ProcessArgs),
		"base_env":          oracleStrings(cfg.BaseEnv),
		"descriptors":       oracleLayout(cfg.Descriptors),
		"runtime_gc":        cfg.Runtime.GC,
		"runtime_memory":    cfg.Runtime.MemoryLimit,
		"lease_interval_ns": int64(cfg.LeaseInterval),
		"lease_ttl_ns":      int64(cfg.LeaseTTL),
		"ready_timeout_ns":  int64(cfg.ReadyTimeout),
		"stop_grace_ns":     int64(cfg.StopGrace),
		"kill_grace_ns":     int64(cfg.KillGrace),
		"has_allocator":     cfg.Allocator != nil,
		"has_launcher":      cfg.Launcher != nil,
		"has_clock":         cfg.Clock != nil,
	}
}

func oraclePlanState(plan ChildPlan) map[string]any {
	return map[string]any{
		"generation":     oracleGeneration(plan.Generation),
		"abi":            plan.Identity.ABI,
		"runtime_gc":     plan.Runtime.GC,
		"runtime_memory": plan.Runtime.MemoryLimit,
		"lease_ms":       plan.LeaseInterval.Milliseconds(),
		"layout":         oracleLayout(plan.Descriptors),
	}
}

func oracleGeneration(generation Generation) map[string]any {
	return map[string]any{
		"worker":       generation.Worker,
		"stream":       generation.Stream,
		"registration": generation.Registration,
		"token":        generation.Token,
	}
}

func oracleLayout(descriptors []Descriptor) []any {
	out := make([]any, 0, len(descriptors))
	for _, descriptor := range descriptors {
		out = append(out, map[string]any{"role": descriptor.Role, "size": descriptor.Size})
	}
	return out
}

func oracleResources(files []Resource) []any {
	out := make([]any, 0, len(files))
	for _, file := range files {
		out = append(out, map[string]any{"role": file.Role(), "size": file.Size()})
	}
	return out
}

func oracleStrings(values []string) []any {
	out := make([]any, 0, len(values))
	for _, value := range values {
		out = append(out, value)
	}
	return out
}

func oracleErr(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

type oracleCase struct {
	Format    string       `json:"format"`
	Component string       `json:"component"`
	Source    string       `json:"source"`
	Stimulus  string       `json:"stimulus"`
	Input     oracleInput  `json:"input"`
	Output    oracleOutput `json:"output"`
}

type oracleInput struct {
	Bytes      string         `json:"bytes"`
	Qualifiers map[string]any `json:"qualifiers"`
	Clock      map[string]any `json:"clock"`
	State      map[string]any `json:"state"`
}

type oracleOutput struct {
	SymbolsWritten []any          `json:"symbols_written"`
	Decisions      []any          `json:"decisions"`
	Cache          map[string]any `json:"cache"`
	Observations   []any          `json:"observations"`
	Counters       map[string]any `json:"counters"`
}

func oracleWrite(t *testing.T, name string, document oracleCase) {
	t.Helper()
	if document.Format == "" {
		document.Format = "helianthus-oracle-bt01-case/1"
	}
	if document.Input.Qualifiers == nil {
		document.Input.Qualifiers = map[string]any{}
	}
	if document.Input.Clock == nil {
		document.Input.Clock = map[string]any{}
	}
	if document.Input.State == nil {
		document.Input.State = map[string]any{}
	}
	if document.Output.SymbolsWritten == nil {
		document.Output.SymbolsWritten = []any{}
	}
	if document.Output.Decisions == nil {
		document.Output.Decisions = []any{}
	}
	if document.Output.Cache == nil {
		document.Output.Cache = map[string]any{}
	}
	if document.Output.Observations == nil {
		document.Output.Observations = []any{}
	}
	if document.Output.Counters == nil {
		document.Output.Counters = map[string]any{}
	}
	encoded, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got := append(encoded, '\n')
	path := filepath.Join("testdata", "oracle", filepath.FromSlash(name))
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("oracle vector %s: %v", name, err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("oracle vector %s differs from the recorded case", name)
	}

}
