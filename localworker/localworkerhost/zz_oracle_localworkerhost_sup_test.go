package localworkerhost

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"testing"
	"time"
)

// TestOracleLocalWorkerHostSupervisor records the pin's supervisor lifecycle:
// report fences, leases, restart, failed launch, stop escalation, unexpected
// exit, duplicate child and allocator mismatch. The stimuli, fixtures and
// injected clock are those of the pin's own tests.
func TestOracleLocalWorkerHostSupervisor(t *testing.T) {
	oracleObserveFences(t)
	oracleLeases(t)
	oracleRestart(t)
	oracleFailedLaunch(t)
	oracleStopEscalation(t)
	oracleUnexpectedExit(t)
	oracleDuplicateAndRecursion(t)
	oracleAllocatorMismatch(t)
}

func oracleStatusOf(status Status) map[string]any {
	return map[string]any{
		"state": fmt.Sprint(status.State), "reason": oracleErr(status.Reason), "generation": oracleGeneration(status.Generation),
		"plans": status.Plans, "pending": status.Pending, "cancelled": status.Cancelled, "launches": status.Launches,
	}
}

func oracleClosed(f *aa27Fixture) []any {
	out := make([]any, 0)
	for _, resource := range f.allocator.all() {
		out = append(out, map[string]any{"role": resource.Role(), "size": resource.Size(), "closes": resource.closed()})
	}
	return out
}

func oracleObserveFences(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Report)
		want   error
	}{
		{"build", func(r *Report) { r.Identity.Build = DigestOf([]byte("aa27-build-b")) }, ErrBuild},
		{"executable", func(r *Report) { r.Identity.Executable = DigestOf([]byte("aa27-image-b")) }, ErrBuild},
		{"abi", func(r *Report) { r.Identity.ABI = ABIVersion + 1 }, ErrABI},
		{"future-worker", func(r *Report) { r.Generation.Worker++ }, ErrGeneration},
		{"stream", func(r *Report) { r.Generation.Stream++ }, ErrGeneration},
		{"registration", func(r *Report) { r.Generation.Registration++ }, ErrGeneration},
		{"token", func(r *Report) { r.Generation.Token++ }, ErrGeneration},
		{"descriptor-role", func(r *Report) { r.Descriptors[1].Role = RoleControl }, ErrDescriptor},
		{"descriptor-size", func(r *Report) { r.Descriptors[1].Size = 8192 }, ErrDescriptor},
		{"descriptor-missing", func(r *Report) { r.Descriptors = r.Descriptors[:1] }, ErrDescriptor},
		{"state-not-passive", func(r *Report) { r.State = WorkerPassive + 1 }, ErrNotPassive},
		{"tx-active", func(r *Report) { r.TXActive = true }, ErrNotPassive},
		{"ready-sequence", func(r *Report) { r.Sequence = 1 }, ErrReport},
		{"unknown-kind", func(r *Report) { r.Kind = 9 }, ErrReport},
	}
	for _, tc := range cases {
		t.Run("fence-"+tc.name, func(t *testing.T) {
			f := newAA27Fixture(t)
			if err := f.sup.Start(); err != nil {
				t.Fatal(err)
			}
			report := Report{Kind: ReportReady, Identity: aa27Self, Generation: f.sup.Status().Generation, Descriptors: append([]Descriptor(nil), aa27Layout...), State: WorkerPassive}
			tc.mutate(&report)
			observeErr := f.sup.Observe(report)
			status := f.sup.Status()
			gen := status.Generation
			planErr := f.sup.PublishPlan(PlanRef{Worker: gen.Worker, Stream: gen.Stream, Registration: gen.Registration, Plan: 1})
			oracleWrite(t, "supervisor/observe-fence-"+tc.name+".json", oracleCase{
				Component: "localworkerhost", Source: "internal/localworkerhost/supervisor.go", Stimulus: "TestAA27ObserveFencesEveryReportFieldMismatch/" + tc.name,
				Input: oracleInput{
					Qualifiers: map[string]any{"mutation": tc.name, "kind": "ready"},
					Clock:      map[string]any{"now": time.Unix(1000, 0).UTC().Format(time.RFC3339Nano)},
					State:      map[string]any{"generation": oracleGeneration(gen), "layout": oracleLayout(aa27Layout), "report": oracleSubmittedReport(report)},
				},
				Output: oracleOutput{Decisions: []any{map[string]any{
					"observe_error": oracleErr(observeErr), "matches_expected": errors.Is(observeErr, tc.want), "status": oracleStatusOf(status),
					"publish_plan_error": oracleErr(planErr), "publish_plan_not_ready": errors.Is(planErr, ErrNotReady),
				}}},
			})
		})
	}
}

func oracleLeases(t *testing.T) {
	lease := func(f *aa27Fixture, sequence uint64) error {
		return f.sup.Observe(Report{Kind: ReportLease, Identity: aa27Self, Generation: f.sup.Status().Generation, State: WorkerPassive, Sequence: sequence})
	}
	recordState := func(name, stimulus string, qualifiers, state map[string]any, decisions []any, f *aa27Fixture) {
		oracleWrite(t, "supervisor/lease-"+name+".json", oracleCase{
			Component: "localworkerhost", Source: "internal/localworkerhost/supervisor.go", Stimulus: "TestAA27LeasesAdvanceStrictlyAndExpireOnInjectedTime/" + stimulus,
			Input: oracleInput{
				Qualifiers: qualifiers,
				Clock:      map[string]any{"start": time.Unix(1000, 0).UTC().Format(time.RFC3339Nano), "lease_interval_ns": int64(100 * time.Millisecond), "lease_ttl_ns": int64(time.Second), "ready_timeout_ns": int64(5 * time.Second)},
				State:      state,
			},
			Output: oracleOutput{Decisions: decisions, Counters: map[string]any{"status": oracleStatusOf(f.sup.Status())}},
		})
	}
	record := func(name, stimulus string, qualifiers map[string]any, decisions []any, f *aa27Fixture) {
		recordState(name, stimulus, qualifiers, nil, decisions, f)
	}
	t.Run("lease-in-order", func(t *testing.T) {
		f := aa27StartReady(t)
		generation := f.sup.Status().Generation
		start := map[string]any{
			"state": "ready", "generation": oracleGeneration(generation), "layout": oracleLayout(aa27Layout),
			"ready_report": oracleSubmittedReport(Report{Kind: ReportReady, Identity: aa27Self, Generation: generation, Descriptors: aa27Layout, State: WorkerPassive}),
		}
		steps := []any{}
		schedule := []any{}
		for sequence := uint64(1); sequence <= 3; sequence++ {
			f.clock.Advance(900 * time.Millisecond)
			leaseErr := lease(f, sequence)
			tickErr := f.sup.Tick()
			steps = append(steps, map[string]any{"sequence": sequence, "advance_ns": int64(900 * time.Millisecond), "lease_error": oracleErr(leaseErr), "tick_error": oracleErr(tickErr)})
			schedule = append(schedule,
				map[string]any{"call": "clock.Advance", "advance_ns": int64(900 * time.Millisecond)},
				map[string]any{"call": "Observe", "report": oracleSubmittedReport(Report{Kind: ReportLease, Identity: aa27Self, Generation: generation, State: WorkerPassive, Sequence: sequence})},
				map[string]any{"call": "Tick"})
		}
		recordState("in-order", "in order", map[string]any{"sequences": []any{1, 2, 3}, "advance_before_each_lease_ns": int64(900 * time.Millisecond)},
			map[string]any{"start": start, "schedule": schedule}, steps, f)
	})
	for name, sequence := range map[string]uint64{"replayed": 0, "skipped": 2} {
		t.Run("lease-"+name, func(t *testing.T) {
			f := aa27StartReady(t)
			err := lease(f, sequence)
			record(name, name, map[string]any{"sequence": sequence}, []any{map[string]any{"lease_error": oracleErr(err), "is_lease": errors.Is(err, ErrLease)}}, f)
		})
	}
	t.Run("lease-late", func(t *testing.T) {
		f := aa27StartReady(t)
		f.clock.Advance(time.Second)
		err := lease(f, 1)
		record("late", "late lease cannot revive", map[string]any{"sequence": 1, "advance_ns": int64(time.Second)}, []any{map[string]any{"lease_error": oracleErr(err), "is_expired": errors.Is(err, ErrLeaseExpired)}}, f)
	})
	t.Run("lease-tick-at-expiry", func(t *testing.T) {
		f := aa27StartReady(t)
		f.clock.Advance(time.Second - time.Nanosecond)
		before := f.sup.Tick()
		f.clock.Advance(time.Nanosecond)
		at := f.sup.Tick()
		record("tick-at-expiry", "tick fences at expiry, not before", map[string]any{"first_advance_ns": int64(time.Second - time.Nanosecond), "second_advance_ns": 1},
			[]any{map[string]any{"tick_before": oracleErr(before), "tick_at": oracleErr(at), "is_expired": errors.Is(at, ErrLeaseExpired)}}, f)
	})
	t.Run("lease-tx-active", func(t *testing.T) {
		f := aa27StartReady(t)
		err := f.sup.Observe(Report{Kind: ReportLease, Identity: aa27Self, Generation: f.sup.Status().Generation, State: WorkerPassive, TXActive: true, Sequence: 1})
		record("tx-active", "tx-active lease", map[string]any{"sequence": 1, "tx_active": true}, []any{map[string]any{"lease_error": oracleErr(err), "is_not_passive": errors.Is(err, ErrNotPassive)}}, f)
	})
	t.Run("lease-readiness-deadline", func(t *testing.T) {
		f := newAA27Fixture(t)
		if err := f.sup.Start(); err != nil {
			t.Fatal(err)
		}
		f.clock.Advance(5 * time.Second)
		err := f.sup.Tick()
		record("readiness-deadline", "readiness deadline", map[string]any{"call": "Tick", "advance_ns": int64(5 * time.Second)}, []any{map[string]any{"tick_error": oracleErr(err), "is_ready_timeout": errors.Is(err, ErrReadyTimeout)}}, f)
	})
	t.Run("lease-late-readiness", func(t *testing.T) {
		f := newAA27Fixture(t)
		if err := f.sup.Start(); err != nil {
			t.Fatal(err)
		}
		f.clock.Advance(5 * time.Second)
		report := Report{Kind: ReportReady, Identity: aa27Self, Generation: f.sup.Status().Generation, Descriptors: aa27Layout, State: WorkerPassive}
		err := f.sup.Observe(report)
		recordState("late-readiness", "late readiness", map[string]any{"call": "Observe", "advance_ns": int64(5 * time.Second)}, map[string]any{"report": oracleSubmittedReport(report)}, []any{map[string]any{"observe_error": oracleErr(err), "is_ready_timeout": errors.Is(err, ErrReadyTimeout)}}, f)
	})
}

func oracleRestart(t *testing.T) {
	t.Run("restart", func(t *testing.T) {
		f := aa27StartReady(t)
		old := f.sup.Status().Generation
		oldReady := Report{Kind: ReportReady, Identity: aa27Self, Generation: old, Descriptors: aa27Layout, State: WorkerPassive}
		// operations lists every call made on the supervisor after it is ready,
		// in call order, with the generation operands each call submits, so
		// that the input alone fixes which fence each stale call exercises.
		operations := make([]any, 0, 16)
		planOperation := func(ref PlanRef) {
			operations = append(operations, map[string]any{"order": len(operations) + 1, "call": "PublishPlan", "worker": ref.Worker, "stream": ref.Stream, "registration": ref.Registration, "plan": ref.Plan})
		}
		commandOperation := func(command Command) {
			operations = append(operations, map[string]any{"order": len(operations) + 1, "call": "Enqueue", "worker": command.Worker, "stream": command.Stream, "token": command.Token, "sequence": command.Sequence})
		}
		oldPlan := PlanRef{Worker: old.Worker, Stream: old.Stream, Registration: old.Registration, Plan: 1}
		planOperation(oldPlan)
		planErr := f.sup.PublishPlan(oldPlan)
		oldCommand := Command{Worker: old.Worker, Stream: old.Stream, Token: old.Token, Sequence: 1}
		commandOperation(oldCommand)
		enqueueErr := f.sup.Enqueue(oldCommand)
		_, first := f.launcher.last()
		operations = append(operations, map[string]any{"order": len(operations) + 1, "call": "Restart"})
		restartErr := f.sup.Restart(context.Background())
		stops, kills := first.counts()
		status := f.sup.Status()
		gen := status.Generation
		drainedAfter := len(f.sup.Drain())
		closedAfterRestart := oracleClosed(f)
		operations = append(operations, map[string]any{"order": len(operations) + 1, "call": "Observe", "report": oracleSubmittedReport(oldReady)})
		predecessorErr := f.sup.Observe(oldReady)
		stateAfterOldReady := fmt.Sprint(f.sup.Status().State)
		operations = append(operations, map[string]any{"order": len(operations) + 1, "call": "Observe", "report": oracleSubmittedReport(f.ready(t))})
		stalePlans := map[string]PlanRef{
			"old worker":       {Worker: old.Worker, Stream: gen.Stream, Registration: gen.Registration, Plan: 2},
			"old stream":       {Worker: gen.Worker, Stream: old.Stream, Registration: gen.Registration, Plan: 2},
			"old registration": {Worker: gen.Worker, Stream: gen.Stream, Registration: old.Registration, Plan: 2},
		}
		staleCommands := map[string]Command{
			"old worker": {Worker: old.Worker, Stream: gen.Stream, Token: gen.Token, Sequence: 1},
			"old stream": {Worker: gen.Worker, Stream: old.Stream, Token: gen.Token, Sequence: 1},
			"old token":  {Worker: gen.Worker, Stream: gen.Stream, Token: old.Token, Sequence: 1},
		}
		stale := make([]any, 0)
		for _, name := range []string{"old worker", "old stream", "old registration"} {
			planOperation(stalePlans[name])
			err := f.sup.PublishPlan(stalePlans[name])
			stale = append(stale, map[string]any{"kind": "plan", "name": name, "error": oracleErr(err), "is_stale": errors.Is(err, ErrStale)})
		}
		for _, name := range []string{"old worker", "old stream", "old token"} {
			commandOperation(staleCommands[name])
			err := f.sup.Enqueue(staleCommands[name])
			stale = append(stale, map[string]any{"kind": "command", "name": name, "error": oracleErr(err), "is_stale": errors.Is(err, ErrStale)})
		}
		fresh := PlanRef{Worker: gen.Worker, Stream: gen.Stream, Registration: gen.Registration, Plan: 1}
		planOperation(fresh)
		freshErr := f.sup.PublishPlan(fresh)
		planOperation(fresh)
		duplicateErr := f.sup.PublishPlan(fresh)
		freshCommand := Command{Worker: gen.Worker, Stream: gen.Stream, Token: gen.Token, Sequence: 1}
		commandOperation(freshCommand)
		commandErr := f.sup.Enqueue(freshCommand)
		operations = append(operations, map[string]any{"order": len(operations) + 1, "call": "Drain"})
		drained := f.sup.Drain()
		oracleWrite(t, "supervisor/restart-advances-every-epoch.json", oracleCase{
			Component: "localworkerhost", Source: "internal/localworkerhost/supervisor.go", Stimulus: "TestAA27RestartAdvancesEveryEpochAndReplaysNothing",
			Input: oracleInput{
				Qualifiers: map[string]any{"first_plan": 1, "first_command_sequence": 1},
				Clock:      map[string]any{"now": time.Unix(1000, 0).UTC().Format(time.RFC3339Nano)},
				State:      map[string]any{"generation_before": oracleGeneration(old), "operations": operations},
			},
			Output: oracleOutput{Decisions: []any{map[string]any{
				"first_plan_error": oracleErr(planErr), "first_enqueue_error": oracleErr(enqueueErr), "restart_error": oracleErr(restartErr),
				"predecessor_stops": stops, "predecessor_kills": kills, "status_after_restart": oracleStatusOf(status), "drain_after_restart": drainedAfter,
				"resources_after_restart": closedAfterRestart,
				"predecessor_ready_error": oracleErr(predecessorErr), "predecessor_ready_is_stale": errors.Is(predecessorErr, ErrStale), "state_after_predecessor_ready": stateAfterOldReady,
				"stale": stale, "fresh_plan_error": oracleErr(freshErr), "duplicate_plan_error": oracleErr(duplicateErr), "duplicate_is_plan": errors.Is(duplicateErr, ErrPlan),
				"fresh_command_error": oracleErr(commandErr), "drained": len(drained), "drained_token": func() uint64 {
					if len(drained) == 0 {
						return 0
					}
					return drained[0].Token
				}(),
			}}},
		})
	})
}

func oracleFailedLaunch(t *testing.T) {
	t.Run("failed-launch", func(t *testing.T) {
		f := newAA27Fixture(t)
		f.launcher.fail = errors.New("aa27 launch refused")
		firstErr := f.sup.Start()
		closed := oracleClosed(f)
		slot := liveChild.Load()
		f.launcher.fail = nil
		secondErr := f.sup.Start()
		oracleWrite(t, "supervisor/failed-launch-consumes-generation.json", oracleCase{
			Component: "localworkerhost", Source: "internal/localworkerhost/supervisor.go", Stimulus: "TestAA27FailedLaunchConsumesItsGeneration",
			Input:  oracleInput{Qualifiers: map[string]any{"launcher_error": "aa27 launch refused"}, Clock: map[string]any{"now": time.Unix(1000, 0).UTC().Format(time.RFC3339Nano)}},
			Output: oracleOutput{Decisions: []any{map[string]any{"first_start_error": oracleErr(firstErr), "resources_after_failure": closed, "slot_claimed_after_failure": slot, "second_start_error": oracleErr(secondErr), "status_after_second_start": oracleStatusOf(f.sup.Status())}}},
		})
	})
}

func oracleStopEscalation(t *testing.T) {
	write := func(name, stimulus string, qualifiers map[string]any, decision map[string]any) {
		oracleWrite(t, "supervisor/stop-"+name+".json", oracleCase{
			Component: "localworkerhost", Source: "internal/localworkerhost/supervisor.go", Stimulus: "TestAA27StopEscalatesAndAnUnreapedChildBlocksEveryStart/" + stimulus,
			Input:  oracleInput{Qualifiers: qualifiers, Clock: map[string]any{"stop_grace_ns": int64(aa27StopGrace), "kill_grace_ns": int64(aa27KillGrace)}},
			Output: oracleOutput{Decisions: []any{decision}},
		})
	}
	t.Run("stop-graceful", func(t *testing.T) {
		f := aa27StartReady(t)
		_, process := f.launcher.last()
		err := f.sup.Stop(context.Background())
		stops, kills := process.counts()
		write("graceful", "graceful", map[string]any{"exit_on_stop": true}, map[string]any{"stop_error": oracleErr(err), "stops": stops, "kills": kills, "status": oracleStatusOf(f.sup.Status())})
	})
	t.Run("stop-kill-after-grace", func(t *testing.T) {
		f := newAA27Fixture(t)
		f.launcher.exitOnStop = false
		f.clock.fire[aa27StopGrace] = true
		if err := f.sup.Start(); err != nil {
			t.Fatal(err)
		}
		_, process := f.launcher.last()
		err := f.sup.Stop(context.Background())
		stops, kills := process.counts()
		write("kill-after-grace", "kill after stop grace", map[string]any{"exit_on_stop": false, "exit_on_kill": true, "stop_grace_fires": true}, map[string]any{"stop_error": oracleErr(err), "stops": stops, "kills": kills, "status": oracleStatusOf(f.sup.Status())})
	})
	t.Run("stop-kill-after-cancelled-context", func(t *testing.T) {
		f := newAA27Fixture(t)
		f.launcher.exitOnStop = false
		if err := f.sup.Start(); err != nil {
			t.Fatal(err)
		}
		_, process := f.launcher.last()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		err := f.sup.Stop(ctx)
		_, kills := process.counts()
		write("kill-after-cancelled-context", "kill after cancelled context", map[string]any{"exit_on_stop": false, "exit_on_kill": true, "context": "cancelled"}, map[string]any{"stop_error": oracleErr(err), "kills": kills, "status": oracleStatusOf(f.sup.Status())})
	})
	t.Run("stop-unreaped", func(t *testing.T) {
		f := newAA27Fixture(t)
		f.launcher.exitOnStop, f.launcher.exitOnKill = false, false
		f.clock.fire[aa27StopGrace], f.clock.fire[aa27KillGrace] = true, true
		if err := f.sup.Start(); err != nil {
			t.Fatal(err)
		}
		_, process := f.launcher.last()
		stopErr := f.sup.Stop(context.Background())
		stateAfterStop := fmt.Sprint(f.sup.Status().State)
		closed := oracleClosed(f)
		startErr := f.sup.Start()
		other, err := New(aa27Config(newAA27Clock(), &aa27Launcher{}, &aa27Allocator{}))
		if err != nil {
			t.Fatal(err)
		}
		otherErr := other.Start()
		f.sup.mu.Lock()
		c := f.sup.child
		f.sup.mu.Unlock()
		process.exit()
		<-c.done
		stateAfterReap := fmt.Sprint(f.sup.Status().State)
		restartErr := f.sup.Start()
		write("unreaped", "unreaped", map[string]any{"exit_on_stop": false, "exit_on_kill": false, "stop_grace_fires": true, "kill_grace_fires": true}, map[string]any{
			"stop_error": oracleErr(stopErr), "is_unreaped": errors.Is(stopErr, ErrUnreaped), "state_after_stop": stateAfterStop, "resources_after_stop": closed,
			"start_while_unreaped": oracleErr(startErr), "second_supervisor_start": oracleErr(otherErr), "state_after_late_reap": stateAfterReap,
			"start_after_reap": oracleErr(restartErr), "worker_epoch_after_reap": f.sup.Status().Generation.Worker,
		})
	})
}

func oracleUnexpectedExit(t *testing.T) {
	t.Run("unexpected-exit", func(t *testing.T) {
		f := aa27StartReady(t)
		gen := f.sup.Status().Generation
		planErr := f.sup.PublishPlan(PlanRef{Worker: gen.Worker, Stream: gen.Stream, Registration: gen.Registration, Plan: 1})
		f.sup.mu.Lock()
		c := f.sup.child
		f.sup.mu.Unlock()
		_, process := f.launcher.last()
		process.exit()
		<-c.done
		status := f.sup.Status()
		oracleWrite(t, "supervisor/unexpected-exit-fences-and-releases.json", oracleCase{
			Component: "localworkerhost", Source: "internal/localworkerhost/supervisor.go", Stimulus: "TestAA27UnexpectedExitFencesAndReleases",
			Input:  oracleInput{Qualifiers: map[string]any{"plan": 1, "child_exits": "unprompted"}, Clock: map[string]any{"now": time.Unix(1000, 0).UTC().Format(time.RFC3339Nano)}},
			Output: oracleOutput{Decisions: []any{map[string]any{"publish_plan_error": oracleErr(planErr), "status": oracleStatusOf(status), "is_exited": errors.Is(status.Reason, ErrExited), "slot_claimed": liveChild.Load()}}},
		})
	})
}

func oracleDuplicateAndRecursion(t *testing.T) {
	t.Run("duplicate-and-recursion", func(t *testing.T) {
		recursion := make([]any, 0)
		for _, tc := range []struct {
			name string
			args []string
		}{{"child argv", []string{"/opt/gateway/bin/gateway", ChildFlag}}, {"flag later", []string{"/opt/gateway/bin/gateway", "-x", ChildFlag}}} {
			cfg := aa27Config(newAA27Clock(), &aa27Launcher{}, &aa27Allocator{})
			cfg.ProcessArgs = tc.args
			_, err := New(cfg)
			recursion = append(recursion, map[string]any{"name": tc.name, "argv": oracleStrings(tc.args), "error": oracleErr(err), "is_recursion": errors.Is(err, ErrRecursion)})
		}
		f := aa27StartReady(t)
		secondStart := f.sup.Start()
		launcher := &aa27Launcher{}
		other, err := New(aa27Config(newAA27Clock(), launcher, &aa27Allocator{}))
		if err != nil {
			t.Fatal(err)
		}
		otherErr := other.Start()
		oracleWrite(t, "supervisor/duplicate-child-and-recursion.json", oracleCase{
			Component: "localworkerhost", Source: "internal/localworkerhost/supervisor.go", Stimulus: "TestAA27DuplicateChildAndRecursionAreRefused",
			Input:  oracleInput{Qualifiers: map[string]any{"child_flag": ChildFlag}},
			Output: oracleOutput{Decisions: []any{map[string]any{"recursion": recursion, "second_start_error": oracleErr(secondStart), "is_running": errors.Is(secondStart, ErrRunning), "other_supervisor_error": oracleErr(otherErr), "is_duplicate": errors.Is(otherErr, ErrDuplicate), "other_launches": len(launcher.specs)}}},
		})
	})
}

func oracleAllocatorMismatch(t *testing.T) {
	for _, name := range []string{"role", "size"} {
		t.Run("allocator-"+name, func(t *testing.T) {
			f := newAA27Fixture(t)
			if name == "role" {
				f.allocator.wrongRole = true
			} else {
				f.allocator.wrongSize = true
			}
			err := f.sup.Start()
			oracleWrite(t, "supervisor/allocator-mismatch-"+name+".json", oracleCase{
				Component: "localworkerhost", Source: "internal/localworkerhost/supervisor.go", Stimulus: "TestAA27AllocatorMismatchRefusesLaunch/" + name,
				Input:  oracleInput{Qualifiers: map[string]any{"mismatch": name}, Clock: map[string]any{"now": time.Unix(1000, 0).UTC().Format(time.RFC3339Nano)}},
				Output: oracleOutput{Decisions: []any{map[string]any{"start_error": oracleErr(err), "is_descriptor": errors.Is(err, ErrDescriptor), "launches": len(f.launcher.specs), "slot_claimed": liveChild.Load(), "resources": oracleClosed(f)}}},
			})
		})
	}
}

// oracleSubmittedReport records the report exactly as handed to Observe, after the mutation of the case.
func oracleSubmittedReport(report Report) map[string]any {
	return map[string]any{
		"kind":        int(report.Kind),
		"state":       int(report.State),
		"tx_active":   report.TXActive,
		"sequence":    report.Sequence,
		"abi":         report.Identity.ABI,
		"build":       hex.EncodeToString(report.Identity.Build[:]),
		"executable":  hex.EncodeToString(report.Identity.Executable[:]),
		"generation":  oracleGeneration(report.Generation),
		"descriptors": oracleLayout(report.Descriptors),
	}
}
